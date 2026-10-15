package client

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// fakePostgresServer simulates a minimal PostgreSQL wire server for unit testing.
func fakePostgresServer(t *testing.T, handler func(net.Conn)) string {
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

func sendAuthOkAndReady(c net.Conn) {
	// Send 'R' AuthenticationOk (length 8, auth type 0)
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_ = binary.Write(c, binary.BigEndian, uint8('R'))
	_ = binary.Write(c, binary.BigEndian, int32(8))
	_ = binary.Write(c, binary.BigEndian, int32(0))

	// Send 'Z' ReadyForQuery (length 5, status 'I')
	_ = binary.Write(c, binary.BigEndian, uint8('Z'))
	_ = binary.Write(c, binary.BigEndian, int32(5))
	_ = binary.Write(c, binary.BigEndian, uint8('I'))
}

func readStartup(c net.Conn) {
	var length int32
	_ = binary.Read(c, binary.BigEndian, &length)
	if length > 4 {
		buf := make([]byte, length-4)
		_, _ = c.Read(buf)
	}
}

func TestPostgresHandshakeAndQuery(t *testing.T) {
	addr := fakePostgresServer(t, func(c net.Conn) {
		defer c.Close()
		readStartup(c)
		sendAuthOkAndReady(c)

		// Read Simple Query
		for {
			var msgType uint8
			if err := binary.Read(c, binary.BigEndian, &msgType); err != nil {
				return
			}
			var length int32
			_ = binary.Read(c, binary.BigEndian, &length)
			queryBuf := make([]byte, length-4)
			_, _ = c.Read(queryBuf)
			query := strings.TrimRight(string(queryBuf), "\x00")

			if strings.Contains(query, "CREATE TABLE") {
				// CommandComplete: 'C'
				tag := "CREATE TABLE\x00"
				_ = binary.Write(c, binary.BigEndian, uint8('C'))
				_ = binary.Write(c, binary.BigEndian, int32(len(tag)+4))
				_, _ = c.Write([]byte(tag))
			} else if strings.Contains(query, "SELECT v FROM") {
				// RowDescription: 'T', DataRow: 'D', CommandComplete: 'C'
				val := "42"
				// DataRow: 1 column
				_ = binary.Write(c, binary.BigEndian, uint8('D'))
				dataLen := int32(2 + 4 + len(val) + 4)
				_ = binary.Write(c, binary.BigEndian, dataLen)
				_ = binary.Write(c, binary.BigEndian, uint16(1)) // 1 col
				_ = binary.Write(c, binary.BigEndian, int32(len(val)))
				_, _ = c.Write([]byte(val))

				tag := "SELECT 1\x00"
				_ = binary.Write(c, binary.BigEndian, uint8('C'))
				_ = binary.Write(c, binary.BigEndian, int32(len(tag)+4))
				_, _ = c.Write([]byte(tag))
			} else {
				tag := "INSERT 0 1\x00"
				_ = binary.Write(c, binary.BigEndian, uint8('C'))
				_ = binary.Write(c, binary.BigEndian, int32(len(tag)+4))
				_, _ = c.Write([]byte(tag))
			}

			// ReadyForQuery: 'Z'
			_ = binary.Write(c, binary.BigEndian, uint8('Z'))
			_ = binary.Write(c, binary.BigEndian, int32(5))
			_ = binary.Write(c, binary.BigEndian, uint8('I'))
		}
	})

	pg := NewPostgres(addr, "test_")
	defer pg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Write
	outWrite := pg.Invoke(ctx, history.Op{Process: 0, F: history.FWrite, Key: "k1", Value: history.Int(42)})
	if outWrite.Type != history.OK {
		t.Fatalf("expected write OK, got %s: %s", outWrite.Type, outWrite.Error)
	}

	// Read
	outRead := pg.Invoke(ctx, history.Op{Process: 0, F: history.FRead, Key: "k1"})
	if outRead.Type != history.OK || outRead.Value == nil || *outRead.Value != 42 {
		t.Fatalf("expected read OK with 42, got %+v", outRead)
	}
}

func TestPostgresErrorClassification(t *testing.T) {
	// Server responds with serialization_failure (SQLSTATE 40001)
	addr := fakePostgresServer(t, func(c net.Conn) {
		defer c.Close()
		readStartup(c)
		sendAuthOkAndReady(c)

		for {
			var msgType uint8
			if err := binary.Read(c, binary.BigEndian, &msgType); err != nil {
				return
			}
			var length int32
			_ = binary.Read(c, binary.BigEndian, &length)
			queryBuf := make([]byte, length-4)
			_, _ = c.Read(queryBuf)

			// ErrorResponse: 'E'
			// Fields: 'C' code "40001", 'M' message "could not serialize access", 0
			var errPayload []byte
			errPayload = append(errPayload, 'C')
			errPayload = append(errPayload, "40001\x00"...)
			errPayload = append(errPayload, 'M')
			errPayload = append(errPayload, "could not serialize access due to concurrent update\x00"...)
			errPayload = append(errPayload, 0)

			_ = binary.Write(c, binary.BigEndian, uint8('E'))
			_ = binary.Write(c, binary.BigEndian, int32(len(errPayload)+4))
			_, _ = c.Write(errPayload)

			// ReadyForQuery
			_ = binary.Write(c, binary.BigEndian, uint8('Z'))
			_ = binary.Write(c, binary.BigEndian, int32(5))
			_ = binary.Write(c, binary.BigEndian, uint8('E')) // in error state
		}
	})

	pg := NewPostgres(addr, "test_")
	defer pg.Close()

	ctx := context.Background()

	// Write encountering 40001 must be classified as Fail (definite abort by DB)
	out := pg.Invoke(ctx, history.Op{Process: 0, F: history.FWrite, Key: "k1", Value: history.Int(99)})
	if out.Type != history.Fail {
		t.Fatalf("serialization failure must be classified as Fail, got: %s", out.Type)
	}

	// Dial failure to an unreachable port must be classified as Fail
	pgUnreach := NewPostgres("127.0.0.1:1", "test_")
	defer pgUnreach.Close()
	outUnreach := pgUnreach.Invoke(ctx, history.Op{Process: 0, F: history.FWrite, Key: "k1", Value: history.Int(99)})
	if outUnreach.Type != history.Fail {
		t.Fatalf("dial failure must be classified as Fail, got: %s", outUnreach.Type)
	}
}
