package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// myFake is a MySQL server for tests. auth is "native", "sha2-fast" or
// "sha2-full" (password "secret"); answer returns the result packets for a
// query (each element one packet payload), or nil to drop the connection.
type myFake struct {
	auth   string
	answer func(q string) [][]byte
	key    *rsa.PrivateKey
}

func startMyFake(t *testing.T, f *myFake) string {
	t.Helper()
	if f.auth == "sha2-full" {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		f.key = k
	}
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
			go f.serve(c)
		}
	}()
	return ln.Addr().String()
}

type myConn struct {
	c   net.Conn
	rd  *bufio.Reader
	seq byte
}

func (m *myConn) write(p []byte) {
	_, _ = m.c.Write(append([]byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), m.seq}, p...))
	m.seq++
}

func (m *myConn) read() []byte {
	var h [4]byte
	if _, err := io.ReadFull(m.rd, h[:]); err != nil {
		return nil
	}
	m.seq = h[3] + 1
	p := make([]byte, int(h[0])|int(h[1])<<8|int(h[2])<<16)
	if _, err := io.ReadFull(m.rd, p); err != nil {
		return nil
	}
	return p
}

var myNonce = []byte("0123456789abcdefghij")

func myOK(status uint16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{0, 1, 0}, status)
}

func myErr(code uint16, state, msg string) []byte {
	return append(append(binary.LittleEndian.AppendUint16([]byte{0xff}, code), '#'), state+msg...)
}

// myRows is a one-column result set; vals nil means NULL.
func myRows(status uint16, vals ...*string) [][]byte {
	out := [][]byte{{1}, []byte("coldef"), {0xfe, 0, 0, 2, 0}}
	for _, v := range vals {
		if v == nil {
			out = append(out, []byte{0xfb})
			continue
		}
		out = append(out, append([]byte{byte(len(*v))}, *v...))
	}
	return append(out, binary.LittleEndian.AppendUint16([]byte{0xfe, 0, 0}, status))
}

func (f *myFake) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	m := &myConn{c: c, rd: bufio.NewReader(c)}
	plugin := "mysql_native_password"
	if f.auth != "native" {
		plugin = "caching_sha2_password"
	}
	hs := []byte{10}
	hs = append(hs, "8.4.0\x00"...)
	hs = append(hs, 1, 0, 0, 0)
	hs = append(hs, myNonce[:8]...)
	hs = append(hs, 0, 0xff, 0xf7, 45, 2, 0, 0xff, 0x81, 21)
	hs = append(hs, make([]byte, 10)...)
	hs = append(hs, myNonce[8:]...)
	hs = append(hs, 0)
	hs = append(append(hs, plugin...), 0)
	m.write(hs)
	resp := m.read()
	if len(resp) < 32 {
		return
	}
	rest := resp[32:]
	user := rest[:bytes.IndexByte(rest, 0)]
	rest = rest[len(user)+1:]
	auth := rest[1 : 1+int(rest[0])]
	_ = user
	switch f.auth {
	case "native":
		if !bytes.Equal(auth, nativePassword("secret", myNonce)) {
			m.write(myErr(1045, "28000", "Access denied"))
			return
		}
	case "sha2-fast":
		if !bytes.Equal(auth, cachingSHA2Password("secret", myNonce)) {
			m.write(myErr(1045, "28000", "Access denied"))
			return
		}
		m.write([]byte{1, 3})
	case "sha2-full":
		m.write([]byte{1, 4})
		if req := m.read(); len(req) != 1 || req[0] != 2 {
			return
		}
		der, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
		m.write(append([]byte{1}, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})...))
		enc := m.read()
		plain, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, f.key, enc, nil) //nolint:gosec
		if err != nil {
			m.write(myErr(1045, "28000", "Access denied"))
			return
		}
		for i := range plain {
			plain[i] ^= myNonce[i%len(myNonce)]
		}
		if string(plain) != "secret\x00" {
			m.write(myErr(1045, "28000", "Access denied"))
			return
		}
	}
	m.write(myOK(2))
	for {
		p := m.read()
		if len(p) == 0 || p[0] != 0x03 {
			return
		}
		q := string(p[1:])
		var pkts [][]byte
		switch {
		case strings.HasPrefix(q, "CREATE TABLE"):
			pkts = [][]byte{myOK(2 | moreResults), myOK(2)}
		case q == "ROLLBACK":
			pkts = [][]byte{myOK(2)}
		default:
			pkts = f.answer(q)
		}
		if pkts == nil {
			return
		}
		for _, pk := range pkts {
			m.write(pk)
		}
	}
}

func str(s string) *string { return &s }

func myClient(addr string) *MySQL {
	return NewMySQL(addr, "t/", ConnOptions{User: "root", Password: "secret"})
}

func TestMySQLAuthentication(t *testing.T) {
	read := func(string) [][]byte { return myRows(2, str("42")) }
	for _, auth := range []string{"native", "sha2-fast", "sha2-full"} {
		m := myClient(startMyFake(t, &myFake{auth: auth, answer: read}))
		out := m.Invoke(context.Background(), history.Op{F: history.FRead, Key: "k"})
		if out.Type != history.OK || out.Value == nil || *out.Value != 42 {
			t.Fatalf("%s: %+v", auth, out)
		}
		m.Close()
	}
	m := NewMySQL(startMyFake(t, &myFake{auth: "sha2-fast", answer: read}), "t/", ConnOptions{Password: "wrong"})
	defer m.Close()
	if out := m.Invoke(context.Background(), history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)}); out.Type != history.Fail {
		t.Fatalf("access denied must be fail: %+v", out)
	}
}

func TestCachingSHA2Scramble(t *testing.T) {
	// Recompute from the definition: the server checks
	// SHA256(XOR(scramble, SHA256(SHA256(SHA256(p)), nonce))) == SHA256(SHA256(p)).
	s := cachingSHA2Password("secret", myNonce)
	h1 := sha256.Sum256([]byte("secret"))
	h2 := sha256.Sum256(h1[:])
	h := sha256.Sum256(append(h2[:], myNonce...))
	for i := range s {
		s[i] ^= h[i]
	}
	if got := sha256.Sum256(s); got != h2 {
		t.Fatal("caching_sha2 scramble does not verify")
	}
}

func TestMySQLAppendTransaction(t *testing.T) {
	var got string
	m := myClient(startMyFake(t, &myFake{auth: "native", answer: func(q string) [][]byte {
		got = q
		more := uint16(2 | moreResults)
		var p [][]byte
		p = append(p, myOK(more), myOK(more))      // SET, START
		p = append(p, myRows(more)...)             // read k1: no row
		p = append(p, myOK(more))                  // append
		p = append(p, myRows(more, str("3,5"))...) // read k2
		return append(p, myOK(2))                  // COMMIT
	}}))
	m.opt.Isolation = "READ COMMITTED"
	defer m.Close()
	op := history.Op{F: history.FTxn, Txn: []history.MicroOp{
		{F: history.MicroRead, Key: "k1"}, {F: history.MicroAppend, Key: "k1", Value: history.Int(9)}, {F: history.MicroRead, Key: "k2"},
	}}
	out := m.Invoke(context.Background(), op)
	if out.Type != history.OK || len(out.Txn[0].Values) != 0 || out.Txn[0].Values == nil || len(out.Txn[2].Values) != 2 {
		t.Fatalf("%+v %+v", out, out.Txn)
	}
	for _, want := range []string{"SET TRANSACTION ISOLATION LEVEL READ COMMITTED; START TRANSACTION;", "CONCAT(v, ',', VALUES(v))", "; COMMIT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("query lacks %q: %s", want, got)
		}
	}
}

func TestMySQLClassification(t *testing.T) {
	more := uint16(2 | moreResults)
	appendTxn := history.Op{F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroAppend, Key: "k", Value: history.Int(1)}}}
	write := history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)}
	cases := []struct {
		name string
		pkts [][]byte
		op   history.Op
		want history.Type
	}{
		{"deadlock before commit", [][]byte{myOK(more), myOK(more), myErr(1213, "40001", "Deadlock")}, appendTxn, history.Fail},
		{"unknown error before commit", [][]byte{myOK(more), myOK(more), myErr(1105, "HY000", "unknown")}, appendTxn, history.Fail},
		{"server shutdown at commit", [][]byte{myOK(more), myOK(more), myOK(more), myErr(1053, "08S01", "shutdown")}, appendTxn, history.Info},
		{"tidb write conflict at commit", [][]byte{myOK(more), myOK(more), myOK(more), myErr(9007, "HY000", "Write conflict")}, appendTxn, history.Fail},
		{"connection dropped", nil, appendTxn, history.Info},
		{"read only server", [][]byte{myErr(1290, "HY000", "--read-only")}, write, history.Fail},
		{"query killed", [][]byte{myErr(1317, "70100", "interrupted")}, write, history.Info},
	}
	for _, c := range cases {
		pkts := c.pkts
		m := myClient(startMyFake(t, &myFake{auth: "native", answer: func(string) [][]byte { return pkts }}))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if out := m.Invoke(ctx, c.op); out.Type != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, out.Type, out.Error, c.want)
		}
		cancel()
		m.Close()
	}
}
