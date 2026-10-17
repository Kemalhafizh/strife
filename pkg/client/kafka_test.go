package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// fakeBroker is a single Kafka broker holding partition 0 of every topic in
// memory. produceErr, when set, is returned for every produce instead of
// appending; dropProduce closes the connection after reading a produce.
type fakeBroker struct {
	mu            sync.Mutex
	addr          string
	logs          map[string][]int64
	metaRetries   int // answer LEADER_NOT_AVAILABLE this many times first
	produceErr    int16
	dropProduce   bool
	maxFetchBatch int
}

func startBroker(t *testing.T) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	b := &fakeBroker{addr: ln.Addr().String(), logs: map[string][]int64{}, maxFetchBatch: 1000}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(c)
		}
	}()
	return b
}

func (b *fakeBroker) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	rd := bufio.NewReader(c)
	for {
		var n int32
		if binary.Read(rd, binary.BigEndian, &n) != nil {
			return
		}
		req := make([]byte, n)
		if _, err := io.ReadFull(rd, req); err != nil {
			return
		}
		r := &kreader{b: req}
		api, _, corr := r.i16(), r.i16(), r.i32()
		r.str() // client id
		resp := b.handle(api, r)
		if resp == nil {
			return
		}
		out := kbuf(nil).i32(int32(4 + len(resp))).i32(corr)
		if _, err := c.Write(append(out, resp...)); err != nil {
			return
		}
	}
}

func (b *fakeBroker) handle(api int16, r *kreader) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	host, port, _ := net.SplitHostPort(b.addr)
	p, _ := strconv.Atoi(port)
	switch api {
	case apiApiVersions:
		return kbuf(nil).i16(0).i32(3).i16(apiProduce).i16(3).i16(11).i16(apiFetch).i16(4).i16(17).i16(apiMetadata).i16(0).i16(12)
	case apiMetadata:
		r.count()
		topic := r.str()
		code := int16(0)
		if b.metaRetries > 0 {
			b.metaRetries--
			code = 5
		}
		out := kbuf(nil).i32(0).i32(1).i32(1).str(host).i32(int32(p)).i16(-1).i16(-1).i32(1)
		out = out.i32(1).i16(0).str(topic).i8(0).i32(1).i16(code).i32(0).i32(1).i32(1).i32(1).i32(1).i32(1)
		return out
	case apiProduce:
		r.str()
		r.i16()
		r.i32()
		r.count()
		topic := r.str()
		r.count()
		r.i32()
		batch := r.bytes()
		if b.dropProduce {
			return nil
		}
		code := b.produceErr
		if code == 0 {
			vals, _, err := parseRecords(batch, 0)
			if err != nil {
				code = 2
			} else {
				b.logs[topic] = append(b.logs[topic], vals...)
			}
		}
		return kbuf(nil).i32(1).str(topic).i32(1).i32(0).i16(code).i64(0).i64(-1).i32(0)
	case apiFetch:
		r.take(4 + 4 + 4 + 4 + 1)
		r.count()
		topic := r.str()
		r.count()
		r.i32()
		from := r.i64()
		log := b.logs[topic]
		var recs []byte
		for off := from; off < int64(len(log)) && off < from+int64(b.maxFetchBatch); off++ {
			batch := recordBatch(log[off], time.Now())
			binary.BigEndian.PutUint64(batch, uint64(off)) // base offset
			recs = append(recs, batch...)
		}
		out := kbuf(nil).i32(0).i32(1).str(topic).i32(1).i32(0).i16(0).i64(int64(len(log))).i64(int64(len(log))).i32(-1)
		out = out.i32(int32(len(recs)))
		return append(out, recs...)
	}
	return nil
}

func kafkaTxn(f string, key string, v int64) history.Op {
	m := history.MicroOp{F: f, Key: key}
	if f == history.MicroAppend {
		m.Value = history.Int(v)
	}
	return history.Op{F: history.FTxn, Txn: []history.MicroOp{m}}
}

func TestKafkaAppendAndReadLog(t *testing.T) {
	b := startBroker(t)
	b.metaRetries = 2                                    // topic auto-creation in progress
	b.maxFetchBatch = 2                                  // force several fetches
	k := NewKafka([]string{"127.0.0.1:1", b.addr}, "p/") // first bootstrap is dead
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for v := int64(1); v <= 5; v++ {
		if out := k.Invoke(ctx, kafkaTxn(history.MicroAppend, "x", v)); out.Type != history.OK {
			t.Fatalf("append %d: %+v", v, out)
		}
	}
	out := k.Invoke(ctx, kafkaTxn(history.MicroRead, "x", 0))
	if out.Type != history.OK || len(out.Txn[0].Values) != 5 || out.Txn[0].Values[4] != 5 || out.Node != b.addr {
		t.Fatalf("read: %+v", out)
	}
	if _, ok := b.logs["p.x"]; !ok {
		t.Fatalf("topic name not sanitised: %v", b.logs)
	}
	if out := k.Invoke(ctx, kafkaTxn(history.MicroRead, "empty", 0)); out.Type != history.OK || out.Txn[0].Values == nil || len(out.Txn[0].Values) != 0 {
		t.Fatalf("empty read: %+v", out)
	}
}

func TestKafkaClassification(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeBroker)
		op   history.Op
		want history.Type
	}{
		{"not leader", func(b *fakeBroker) { b.produceErr = 6 }, kafkaTxn(history.MicroAppend, "x", 1), history.Fail},
		{"not enough replicas", func(b *fakeBroker) { b.produceErr = 19 }, kafkaTxn(history.MicroAppend, "x", 1), history.Fail},
		{"timed out", func(b *fakeBroker) { b.produceErr = 7 }, kafkaTxn(history.MicroAppend, "x", 1), history.Info},
		{"after append", func(b *fakeBroker) { b.produceErr = 20 }, kafkaTxn(history.MicroAppend, "x", 1), history.Info},
		{"connection dropped", func(b *fakeBroker) { b.dropProduce = true }, kafkaTxn(history.MicroAppend, "x", 1), history.Info},
		{"no leader ever", func(b *fakeBroker) { b.metaRetries = 1000 }, kafkaTxn(history.MicroAppend, "x", 1), history.Fail},
	}
	for _, c := range cases {
		b := startBroker(t)
		c.set(b)
		k := NewKafka([]string{b.addr}, "")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if out := k.Invoke(ctx, c.op); out.Type != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, out.Type, out.Error, c.want)
		}
		cancel()
		k.Close()
	}
	k := NewKafka([]string{"127.0.0.1:1"}, "")
	if out := k.Invoke(context.Background(), kafkaTxn(history.MicroAppend, "x", 1)); out.Type != history.Fail {
		t.Fatalf("unreachable broker must be fail, got %s", out.Type)
	}
	multi := history.Op{F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroRead, Key: "a"}, {F: history.MicroRead, Key: "b"}}}
	if out := k.Invoke(context.Background(), multi); out.Type != history.Fail || !strings.Contains(out.Error, "txn_len 1") {
		t.Fatalf("multi-operation transactions must be refused: %+v", out)
	}
}

func TestRecordBatchRoundTrip(t *testing.T) {
	var data []byte
	for v := int64(10); v < 13; v++ {
		batch := recordBatch(v, time.Unix(1700000000, 0))
		binary.BigEndian.PutUint64(batch, uint64(v-10))
		data = append(data, batch...)
	}
	vals, next, err := parseRecords(data, 1)
	if err != nil || len(vals) != 2 || vals[0] != 11 || next != 3 {
		t.Fatalf("got %v %d %v", vals, next, err)
	}
	// A truncated trailing batch is ignored, as brokers may send one.
	vals, next, err = parseRecords(data[:len(data)-5], 0)
	if err != nil || len(vals) != 2 || next != 2 {
		t.Fatalf("truncated: %v %d %v", vals, next, err)
	}
	// A corrupted record fails the CRC check.
	bad := append([]byte(nil), data...)
	bad[len(bad)-3] ^= 0xff
	if _, _, err := parseRecords(bad, 0); err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("corruption not detected: %v", err)
	}
}
