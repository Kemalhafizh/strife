package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Kafka speaks the Kafka wire protocol with the standard library only. A key
// is a topic with one partition, and the partition's log is a list: an
// append produces one record (acks=all) and a read fetches the whole log up
// to the high watermark. Histories are therefore list-append histories of
// single-operation transactions, which the append model checks for lost or
// reordered acknowledged writes, duplicates and stale reads.
//
// The client negotiates versions with ApiVersions and uses Metadata v4,
// Produce v3 and Fetch v4 (record batch format v2): the oldest versions
// Kafka 4.0 still accepts (KIP-896) and the newest that need no tagged
// fields. Requests go to the partition leader found through Metadata.
//
// Classification:
//   - reads never change state: fail;
//   - produce errors that reject the record before it is appended (not
//     leader, unknown topic, not enough replicas, too large, corrupt): fail;
//   - errors after the leader may have appended it (request timed out, not
//     enough replicas after append, storage errors) and lost connections
//     after sending: info;
//   - failures before the request left this client: fail.
type Kafka struct {
	bootstrap []string // tried in order for metadata
	prefix    string
	conns     map[string]*kafkaConn // by broker address
	leader    map[string]string     // topic -> leader address
	corr      int32
}

type kafkaConn struct {
	addr     string
	conn     net.Conn
	rd       *bufio.Reader
	versions map[int16][2]int16 // api key -> [min, max]
}

// API keys and the versions Strife speaks.
const (
	apiProduce     int16 = 0
	apiFetch       int16 = 1
	apiMetadata    int16 = 3
	apiApiVersions int16 = 18

	produceVersion  int16 = 3
	fetchVersion    int16 = 4
	metadataVersion int16 = 4
)

// NewKafka returns a client that bootstraps from the given brokers
// ("10.0.0.1:9092"), trying them in order.
func NewKafka(bootstrap []string, prefix string) *Kafka {
	return &Kafka{bootstrap: bootstrap, prefix: prefix, conns: map[string]*kafkaConn{}, leader: map[string]string{}}
}

// Close implements Client.
func (k *Kafka) Close() error {
	for a, c := range k.conns {
		_ = c.conn.Close()
		delete(k.conns, a)
	}
	return nil
}

type kafkaError struct{ code int16 }

func (e *kafkaError) Error() string {
	return fmt.Sprintf("kafka: %s (%d)", kafkaErrorName(e.code), e.code)
}

// ambiguous reports whether a produce failing with this code may still have
// appended the record.
func (e *kafkaError) ambiguous() bool {
	switch e.code {
	case 7, // REQUEST_TIMED_OUT
		20, // NOT_ENOUGH_REPLICAS_AFTER_APPEND
		56, // KAFKA_STORAGE_ERROR
		-1: // UNKNOWN_SERVER_ERROR
		return true
	}
	return false
}

func kafkaErrorName(code int16) string {
	names := map[int16]string{
		-1: "UNKNOWN_SERVER_ERROR", 1: "OFFSET_OUT_OF_RANGE", 2: "CORRUPT_MESSAGE", 3: "UNKNOWN_TOPIC_OR_PARTITION",
		5: "LEADER_NOT_AVAILABLE", 6: "NOT_LEADER_OR_FOLLOWER", 7: "REQUEST_TIMED_OUT", 10: "MESSAGE_TOO_LARGE",
		19: "NOT_ENOUGH_REPLICAS", 20: "NOT_ENOUGH_REPLICAS_AFTER_APPEND", 35: "UNSUPPORTED_VERSION",
		56: "KAFKA_STORAGE_ERROR", 74: "FENCED_LEADER_EPOCH", 75: "UNKNOWN_LEADER_EPOCH",
	}
	if n, ok := names[code]; ok {
		return n
	}
	return "ERROR_" + strconv.Itoa(int(code))
}

// topic maps a key to a legal topic name.
func (k *Kafka) topic(key string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '.'
	}, k.prefix+key)
}

// ---------------------------------------------------------------------------
// Encoding helpers.

type kbuf []byte

func (b kbuf) i8(v int8) kbuf   { return append(b, byte(v)) }
func (b kbuf) i16(v int16) kbuf { return binary.BigEndian.AppendUint16(b, uint16(v)) }
func (b kbuf) i32(v int32) kbuf { return binary.BigEndian.AppendUint32(b, uint32(v)) }
func (b kbuf) i64(v int64) kbuf { return binary.BigEndian.AppendUint64(b, uint64(v)) }
func (b kbuf) str(s string) kbuf {
	return append(b.i16(int16(len(s))), s...)
}

type kreader struct {
	b   []byte
	err error
}

func (r *kreader) take(n int) []byte {
	if r.err != nil || n < 0 || len(r.b) < n {
		r.err = io.ErrUnexpectedEOF
		return make([]byte, 8) // enough for any fixed-size read; r.err is set
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}
func (r *kreader) i8() int8   { return int8(r.take(1)[0]) }
func (r *kreader) i16() int16 { return int16(binary.BigEndian.Uint16(r.take(2))) }
func (r *kreader) i32() int32 { return int32(binary.BigEndian.Uint32(r.take(4))) }
func (r *kreader) i64() int64 { return int64(binary.BigEndian.Uint64(r.take(8))) }
func (r *kreader) str() string {
	n := r.i16()
	if n < 0 {
		return ""
	}
	return string(r.take(int(n)))
}
func (r *kreader) bytes() []byte {
	n := r.i32()
	if n < 0 {
		return nil
	}
	return r.take(int(n))
}
func (r *kreader) varint() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b) // zigzag, like Kafka's varints
	if n <= 0 {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	r.b = r.b[n:]
	return v
}

// count reads an array length, rejecting absurd values from corrupt input.
func (r *kreader) count() int {
	n := r.i32()
	if n < 0 {
		return 0
	}
	if int(n) > len(r.b) {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	return int(n)
}

// ---------------------------------------------------------------------------
// Connections and requests.

func (k *Kafka) connect(ctx context.Context, addr string) (*kafkaConn, error) {
	if c, ok := k.conns[addr]; ok {
		return c, nil
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &kafkaConn{addr: addr, conn: nc, rd: bufio.NewReader(nc)}
	k.conns[addr] = c
	// ApiVersions v0 is understood by every broker.
	body, err := k.request(ctx, c, apiApiVersions, 0, nil)
	if err != nil {
		k.forget(addr)
		return nil, err
	}
	r := &kreader{b: body}
	if code := r.i16(); code != 0 {
		k.forget(addr)
		return nil, &kafkaError{code}
	}
	c.versions = map[int16][2]int16{}
	for n := r.count(); n > 0; n-- {
		key, lo, hi := r.i16(), r.i16(), r.i16()
		c.versions[key] = [2]int16{lo, hi}
	}
	if r.err != nil {
		k.forget(addr)
		return nil, fmt.Errorf("kafka: ApiVersions: %w", r.err)
	}
	for key, v := range map[int16]int16{apiProduce: produceVersion, apiFetch: fetchVersion, apiMetadata: metadataVersion} {
		if r, ok := c.versions[key]; !ok || v < r[0] || v > r[1] {
			k.forget(addr)
			return nil, fmt.Errorf("kafka: broker %s does not support api %d v%d (supports %v)", addr, key, v, c.versions[key])
		}
	}
	return c, nil
}

func (k *Kafka) forget(addr string) {
	if c, ok := k.conns[addr]; ok {
		_ = c.conn.Close()
		delete(k.conns, addr)
	}
	for t, a := range k.leader {
		if a == addr {
			delete(k.leader, t)
		}
	}
}

// request sends one request and returns the response body. Errors after the
// request was written are sentErrors.
func (k *Kafka) request(ctx context.Context, c *kafkaConn, api, version int16, body []byte) ([]byte, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Time{})
	}
	k.corr++
	hdr := kbuf(nil).i16(api).i16(version).i32(k.corr).str("strife")
	msg := kbuf(nil).i32(int32(len(hdr) + len(body)))
	msg = append(append(msg, hdr...), body...)
	if _, err := c.conn.Write(msg); err != nil {
		k.forget(c.addr)
		return nil, &sentError{err}
	}
	var n int32
	if err := binary.Read(c.rd, binary.BigEndian, &n); err != nil {
		k.forget(c.addr)
		return nil, &sentError{err}
	}
	if n < 4 || n > 64<<20 {
		k.forget(c.addr)
		return nil, &sentError{fmt.Errorf("kafka: bad response length %d", n)}
	}
	resp, err := readN(c.rd, int(n))
	if err != nil {
		k.forget(c.addr)
		return nil, &sentError{err}
	}
	if got := int32(binary.BigEndian.Uint32(resp)); got != k.corr {
		k.forget(c.addr)
		return nil, &sentError{fmt.Errorf("kafka: correlation id %d, want %d", got, k.corr)}
	}
	return resp[4:], nil
}

// leaderOf returns a connection to the leader of topic's partition 0,
// creating the topic if the broker allows it.
func (k *Kafka) leaderOf(ctx context.Context, topic string) (*kafkaConn, error) {
	if addr, ok := k.leader[topic]; ok {
		if c, err := k.connect(ctx, addr); err == nil {
			return c, nil
		}
	}
	var (
		brokers map[int32]string
		leader  int32
		code    int16
	)
	// Auto-creating a topic and electing its leader take a moment: retry
	// while the broker reports the topic unknown or leaderless.
	for attempt := 0; ; attempt++ {
		var body []byte
		var err error
		for _, addr := range k.bootstrap {
			var boot *kafkaConn
			if boot, err = k.connect(ctx, addr); err != nil {
				continue
			}
			req := kbuf(nil).i32(1).str(topic).i8(1) // topics, allow_auto_topic_creation
			if body, err = k.request(ctx, boot, apiMetadata, metadataVersion, req); err == nil {
				break
			}
		}
		if err != nil {
			return nil, err
		}
		if brokers, leader, code, err = parseMetadata(body, topic); err != nil {
			return nil, err
		}
		if code == 0 && leader >= 0 {
			break
		}
		if (code != 3 && code != 5 && code != 0) || attempt >= 20 {
			if code == 0 {
				code = 5
			}
			return nil, &kafkaError{code}
		}
		select {
		case <-time.After(time.Duration(25*(attempt+1)) * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	addr, ok := brokers[leader]
	if !ok {
		return nil, &kafkaError{5} // LEADER_NOT_AVAILABLE
	}
	k.leader[topic] = addr
	return k.connect(ctx, addr)
}

// parseMetadata reads a Metadata v4 response: broker addresses by id, and
// the leader and error of the topic's partition 0.
func parseMetadata(body []byte, topic string) (map[int32]string, int32, int16, error) {
	r := &kreader{b: body}
	r.i32() // throttle
	brokers := map[int32]string{}
	for n := r.count(); n > 0; n-- {
		id, host, port := r.i32(), r.str(), r.i32()
		r.str() // rack
		brokers[id] = net.JoinHostPort(host, strconv.Itoa(int(port)))
	}
	r.str() // cluster id
	r.i32() // controller
	leader, code := int32(-1), int16(3)
	for n := r.count(); n > 0; n-- {
		tcode, name := r.i16(), r.str()
		r.i8() // internal
		for p := r.count(); p > 0; p-- {
			pcode, idx, l := r.i16(), r.i32(), r.i32()
			for range r.count() { // replicas
				r.i32()
			}
			for range r.count() { // isr
				r.i32()
			}
			if name == topic && idx == 0 {
				leader, code = l, pcode
			}
		}
		if name == topic && tcode != 0 {
			code = tcode
		}
	}
	if r.err != nil {
		return nil, 0, 0, &sentError{fmt.Errorf("kafka: Metadata: %w", r.err)}
	}
	return brokers, leader, code, nil
}

// ---------------------------------------------------------------------------
// Records.

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// recordBatch encodes one record with value v as a v2 record batch.
func recordBatch(v int64, now time.Time) []byte {
	val := strconv.FormatInt(v, 10)
	rec := kbuf(nil).i8(0)             // attributes
	rec = binary.AppendVarint(rec, 0)  // timestamp delta
	rec = binary.AppendVarint(rec, 0)  // offset delta
	rec = binary.AppendVarint(rec, -1) // null key
	rec = binary.AppendVarint(rec, int64(len(val)))
	rec = append(rec, val...)
	rec = binary.AppendVarint(rec, 0) // headers
	ms := now.UnixMilli()
	tail := kbuf(nil).i16(0).i32(0).i64(ms).i64(ms).i64(-1).i16(-1).i32(-1).i32(1)
	tail = append(binary.AppendVarint(tail, int64(len(rec))), rec...)
	inner := kbuf(nil).i32(-1).i8(2) // partition leader epoch, magic
	inner = inner.i32(int32(crc32.Checksum(tail, castagnoli)))
	inner = append(inner, tail...)
	return append(kbuf(nil).i64(0).i32(int32(len(inner))), inner...)
}

// parseRecords decodes the record batches in a fetch response and returns
// the values of the records at offsets >= from, plus the offset after the
// last complete record seen.
func parseRecords(data []byte, from int64) ([]int64, int64, error) {
	var vals []int64
	next := from
	for len(data) >= 12 {
		base := int64(binary.BigEndian.Uint64(data))
		size := int(int32(binary.BigEndian.Uint32(data[8:])))
		if size < 0 || len(data) < 12+size {
			break // the broker may truncate the last batch
		}
		r := &kreader{b: data[12 : 12+size]}
		data = data[12+size:]
		r.i32() // leader epoch
		if magic := r.i8(); magic != 2 {
			return nil, 0, fmt.Errorf("kafka: record batch magic %d (want 2)", magic)
		}
		crc := uint32(r.i32())
		if r.err == nil && crc32.Checksum(r.b, castagnoli) != crc {
			return nil, 0, fmt.Errorf("kafka: record batch at offset %d has a bad CRC", base)
		}
		attrs := r.i16()
		lastDelta := r.i32()
		r.take(8 + 8 + 8 + 2 + 4) // timestamps, producer id/epoch, base sequence
		count := r.count()
		if attrs&0x07 != 0 {
			return nil, 0, fmt.Errorf("kafka: compressed batches are not supported (attributes %#x)", attrs)
		}
		control := attrs&0x20 != 0
		for i := 0; i < count; i++ {
			n := r.varint()
			rr := &kreader{b: r.take(int(n))}
			rr.i8()     // attributes
			rr.varint() // timestamp delta
			off := base + rr.varint()
			if kl := rr.varint(); kl > 0 {
				rr.take(int(kl))
			}
			vl := rr.varint()
			val := rr.take(int(max(vl, 0)))
			if rr.err != nil || r.err != nil {
				return nil, 0, fmt.Errorf("kafka: malformed record at offset %d", off)
			}
			if control || off < from {
				continue
			}
			v, err := strconv.ParseInt(string(val), 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("kafka: record at offset %d is not a Strife value: %q", off, val)
			}
			vals = append(vals, v)
		}
		if r.err != nil {
			return nil, 0, fmt.Errorf("kafka: malformed record batch at offset %d", base)
		}
		next = max(next, base+int64(lastDelta)+1)
	}
	return vals, next, nil
}

func (k *Kafka) produce(ctx context.Context, topic string, v int64) error {
	c, err := k.leaderOf(ctx, topic)
	if err != nil {
		return &notSent{err}
	}
	batch := recordBatch(v, time.Now())
	req := kbuf(nil).i16(-1).i16(-1).i32(int32(timeoutMS(ctx))) // transactional id, acks=all, timeout
	req = req.i32(1).str(topic).i32(1).i32(0).i32(int32(len(batch)))
	req = append(req, batch...)
	body, err := k.request(ctx, c, apiProduce, produceVersion, req)
	if err != nil {
		return err
	}
	r := &kreader{b: body}
	code := int16(-1)
	for n := r.count(); n > 0; n-- {
		r.str()
		for p := r.count(); p > 0; p-- {
			r.i32()
			code = r.i16()
			r.i64() // base offset
			r.i64() // log append time
		}
	}
	if r.err != nil {
		return &sentError{fmt.Errorf("kafka: Produce response: %w", r.err)}
	}
	if code != 0 {
		if code == 6 || code == 5 || code == 74 {
			delete(k.leader, topic)
		}
		return &kafkaError{code}
	}
	return nil
}

// readLog fetches every record of topic's partition 0 up to the high
// watermark.
func (k *Kafka) readLog(ctx context.Context, topic string) ([]int64, error) {
	c, err := k.leaderOf(ctx, topic)
	if err != nil {
		return nil, err
	}
	vals := []int64{}
	var offset int64
	for {
		req := kbuf(nil).i32(-1).i32(0).i32(1).i32(8 << 20).i8(0) // replica, max wait, min bytes, max bytes, read_uncommitted
		req = req.i32(1).str(topic).i32(1).i32(0).i64(offset).i32(4 << 20)
		body, err := k.request(ctx, c, apiFetch, fetchVersion, req)
		if err != nil {
			return nil, err
		}
		r := &kreader{b: body}
		r.i32() // throttle
		var (
			code int16 = -1
			hw   int64
			recs []byte
		)
		for n := r.count(); n > 0; n-- {
			r.str()
			for p := r.count(); p > 0; p-- {
				r.i32()
				code, hw = r.i16(), r.i64()
				r.i64() // last stable offset
				for a := r.count(); a > 0; a-- {
					r.i64()
					r.i64()
				}
				recs = r.bytes()
			}
		}
		if r.err != nil {
			return nil, fmt.Errorf("kafka: Fetch response: %w", r.err)
		}
		if code != 0 {
			if code == 6 || code == 5 || code == 74 {
				delete(k.leader, topic)
			}
			return nil, &kafkaError{code}
		}
		got, next, err := parseRecords(recs, offset)
		if err != nil {
			return nil, err
		}
		vals = append(vals, got...)
		if next >= hw || next == offset {
			if next < hw {
				return nil, fmt.Errorf("kafka: fetch made no progress at offset %d (high watermark %d)", offset, hw)
			}
			return vals, nil
		}
		offset = next
	}
}

func timeoutMS(ctx context.Context) int64 {
	if dl, ok := ctx.Deadline(); ok {
		return max(1, time.Until(dl).Milliseconds())
	}
	return 30000
}

// Invoke implements Client. Only single-operation list-append transactions
// are supported: Kafka has no multi-topic atomicity without its transaction
// protocol.
func (k *Kafka) Invoke(ctx context.Context, op history.Op) history.Op {
	if op.F != history.FTxn || len(op.Txn) != 1 {
		out := complete(op, history.Fail)
		out.Error = "kafka: only single-operation append/read transactions are supported (workload.txn_len 1)"
		return out
	}
	m := op.Txn[0]
	topic := k.topic(m.Key)
	out := complete(op, history.OK)
	var err error
	switch m.F {
	case history.MicroAppend:
		err = k.produce(ctx, topic, *m.Value)
	case history.MicroRead:
		var vals []int64
		if vals, err = k.readLog(ctx, topic); err == nil {
			out.Txn[0].Values = vals
		}
	default:
		err = &notSent{fmt.Errorf("kafka: unsupported micro-op %q", m.F)}
	}
	if err == nil {
		out.Node = k.leader[topic] // the broker that answered, for diagnosis
		return out
	}
	t := history.Info
	var ns *notSent
	var ke *kafkaError
	switch {
	case m.F == history.MicroRead, errors.As(err, &ns):
		t = history.Fail
	case errors.As(err, &ke) && !ke.ambiguous():
		t = history.Fail
	}
	out = complete(op, t)
	out.Error = err.Error()
	return out
}
