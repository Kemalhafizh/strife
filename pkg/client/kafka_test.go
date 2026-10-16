package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// fakeKafkaServer simulates a minimal Kafka broker responding to Produce and Fetch requests.
func fakeKafkaServer(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handler(c)
		}
	}()
	return ln.Addr().String()
}

func TestKafkaProduceAndFetch(t *testing.T) {
	var storedVal int64 = 777

	addr := fakeKafkaServer(t, func(c net.Conn) {
		defer c.Close()
		for {
			var length int32
			if err := binary.Read(c, binary.BigEndian, &length); err != nil || length <= 0 {
				return
			}
			reqBuf := make([]byte, length)
			if _, err := io.ReadFull(c, reqBuf); err != nil {
				return
			}
			r := bytes.NewReader(reqBuf)

			var apiKey, apiVersion int16
			var corrID int32
			_ = binary.Read(r, binary.BigEndian, &apiKey)
			_ = binary.Read(r, binary.BigEndian, &apiVersion)
			_ = binary.Read(r, binary.BigEndian, &corrID)

			switch apiKey {
			case 0: // Produce
				// Build Produce Response v0:
				// corrID(int32), topicCount(int32 1), topicName("test"), partitionCount(int32 1),
				// partition(int32 0), errorCode(int16 0), offset(int64 100)
				var resp bytes.Buffer
				_ = binary.Write(&resp, binary.BigEndian, corrID)
				_ = binary.Write(&resp, binary.BigEndian, int32(1))
				_ = binary.Write(&resp, binary.BigEndian, int16(4))
				resp.WriteString("test")
				_ = binary.Write(&resp, binary.BigEndian, int32(1))
				_ = binary.Write(&resp, binary.BigEndian, int32(0))
				_ = binary.Write(&resp, binary.BigEndian, int16(0)) // errCode 0 (NONE)
				_ = binary.Write(&resp, binary.BigEndian, int64(100))

				respLen := int32(resp.Len())
				_ = binary.Write(c, binary.BigEndian, respLen)
				_, _ = c.Write(resp.Bytes())

			case 1: // Fetch
				// Build MessageSet containing storedVal
				valStr := []byte("777")
				var msgBody bytes.Buffer
				msgBody.WriteByte(0)
				msgBody.WriteByte(0)
				_ = binary.Write(&msgBody, binary.BigEndian, int32(-1))
				_ = binary.Write(&msgBody, binary.BigEndian, int32(len(valStr)))
				msgBody.Write(valStr)

				checksum := crc32.ChecksumIEEE(msgBody.Bytes())
				var msg bytes.Buffer
				_ = binary.Write(&msg, binary.BigEndian, checksum)
				msg.Write(msgBody.Bytes())

				var msgSet bytes.Buffer
				_ = binary.Write(&msgSet, binary.BigEndian, int64(0)) // offset 0
				_ = binary.Write(&msgSet, binary.BigEndian, int32(msg.Len()))
				msgSet.Write(msg.Bytes())

				// Build Fetch Response v0:
				// corrID(int32), topicCount(int32 1), topicName("test"), partitionCount(int32 1),
				// partition(int32 0), errorCode(int16 0), hwm(int64 1), msgSetSize(int32), msgSet
				var resp bytes.Buffer
				_ = binary.Write(&resp, binary.BigEndian, corrID)
				_ = binary.Write(&resp, binary.BigEndian, int32(1))
				_ = binary.Write(&resp, binary.BigEndian, int16(4))
				resp.WriteString("test")
				_ = binary.Write(&resp, binary.BigEndian, int32(1))
				_ = binary.Write(&resp, binary.BigEndian, int32(0))
				_ = binary.Write(&resp, binary.BigEndian, int16(0))
				_ = binary.Write(&resp, binary.BigEndian, int64(1))
				_ = binary.Write(&resp, binary.BigEndian, int32(msgSet.Len()))
				resp.Write(msgSet.Bytes())

				respLen := int32(resp.Len())
				_ = binary.Write(c, binary.BigEndian, respLen)
				_, _ = c.Write(resp.Bytes())
			}
		}
	})

	k := NewKafka(addr, "")
	defer k.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Enqueue (Produce)
	outEnq := k.Invoke(ctx, history.Op{Process: 0, F: history.FEnqueue, Key: "test", Value: history.Int(storedVal)})
	if outEnq.Type != history.OK {
		t.Fatalf("expected produce OK, got %s: %s", outEnq.Type, outEnq.Error)
	}

	// Dequeue (Fetch)
	outDeq := k.Invoke(ctx, history.Op{Process: 0, F: history.FDequeue, Key: "test"})
	if outDeq.Type != history.OK || outDeq.Value == nil || *outDeq.Value != storedVal {
		t.Fatalf("expected fetch OK with %d, got %+v", storedVal, outDeq)
	}
}

func TestKafkaErrorClassification(t *testing.T) {
	// Server returns LEADER_NOT_AVAILABLE (error code 5)
	addr := fakeKafkaServer(t, func(c net.Conn) {
		defer c.Close()
		for {
			var length int32
			if err := binary.Read(c, binary.BigEndian, &length); err != nil || length <= 0 {
				return
			}
			reqBuf := make([]byte, length)
			if _, err := io.ReadFull(c, reqBuf); err != nil {
				return
			}
			r := bytes.NewReader(reqBuf)

			var apiKey, apiVersion int16
			var corrID int32
			_ = binary.Read(r, binary.BigEndian, &apiKey)
			_ = binary.Read(r, binary.BigEndian, &apiVersion)
			_ = binary.Read(r, binary.BigEndian, &corrID)

			var resp bytes.Buffer
			_ = binary.Write(&resp, binary.BigEndian, corrID)
			_ = binary.Write(&resp, binary.BigEndian, int32(1))
			_ = binary.Write(&resp, binary.BigEndian, int16(4))
			resp.WriteString("test")
			_ = binary.Write(&resp, binary.BigEndian, int32(1))
			_ = binary.Write(&resp, binary.BigEndian, int32(0))
			_ = binary.Write(&resp, binary.BigEndian, int16(5)) // LEADER_NOT_AVAILABLE
			_ = binary.Write(&resp, binary.BigEndian, int64(-1))

			respLen := int32(resp.Len())
			_ = binary.Write(c, binary.BigEndian, respLen)
			_, _ = c.Write(resp.Bytes())
		}
	})

	k := NewKafka(addr, "")
	defer k.Close()

	ctx := context.Background()

	// Produce encountering error code 5 must be classified as Fail (rejected by broker)
	out := k.Invoke(ctx, history.Op{Process: 0, F: history.FEnqueue, Key: "test", Value: history.Int(10)})
	if out.Type != history.Fail {
		t.Fatalf("leader not available must be classified as Fail, got: %s", out.Type)
	}

	// Dial failure must be classified as Fail
	kUnreach := NewKafka("127.0.0.1:1", "")
	defer kUnreach.Close()
	outUnreach := kUnreach.Invoke(ctx, history.Op{Process: 0, F: history.FEnqueue, Key: "test", Value: history.Int(10)})
	if outUnreach.Type != history.Fail {
		t.Fatalf("dial failure must be classified as Fail, got: %s", outUnreach.Type)
	}
}
