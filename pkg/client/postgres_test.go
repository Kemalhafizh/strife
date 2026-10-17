package client

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// pgMsg frames a backend message.
func pgMsg(typ byte, body []byte) []byte {
	return append(binary.BigEndian.AppendUint32([]byte{typ}, uint32(len(body)+4)), body...)
}

func pgTag(tag string) []byte { return pgMsg('C', append([]byte(tag), 0)) }

func pgRow(vals ...string) []byte {
	b := binary.BigEndian.AppendUint16(nil, uint16(len(vals)))
	for _, v := range vals {
		b = binary.BigEndian.AppendUint32(b, uint32(len(v)))
		b = append(b, v...)
	}
	return pgMsg('D', b)
}

func pgErr(code, msg string) []byte {
	return pgMsg('E', []byte("C"+code+"\x00M"+msg+"\x00\x00"))
}

func pgReady(status byte) []byte { return pgMsg('Z', []byte{status}) }

func pgAuth(code uint32, data string) []byte {
	return pgMsg('R', append(binary.BigEndian.AppendUint32(nil, code), data...))
}

// fakePG serves the PostgreSQL protocol: auth is "trust", "md5" or "scram"
// (password "secret"), and answer returns the backend messages for a query.
// A nil answer closes the connection without replying.
func fakePG(t *testing.T, auth string, answer func(q string) []byte) string {
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
			go servePG(c, auth, answer)
		}
	}()
	return ln.Addr().String()
}

func servePG(c net.Conn, auth string, answer func(q string) []byte) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	rd := bufio.NewReader(c)
	var n uint32
	if binary.Read(rd, binary.BigEndian, &n) != nil {
		return
	}
	startup := make([]byte, n-4)
	if _, err := io.ReadFull(rd, startup); err != nil {
		return
	}
	readMsg := func() (byte, string) {
		typ, err := rd.ReadByte()
		if err != nil {
			return 0, ""
		}
		var l uint32
		if binary.Read(rd, binary.BigEndian, &l) != nil {
			return 0, ""
		}
		b := make([]byte, l-4)
		if _, err := io.ReadFull(rd, b); err != nil {
			return 0, ""
		}
		return typ, string(b)
	}
	switch auth {
	case "md5":
		_, _ = c.Write(pgAuth(5, "abcd"))
		_, pw := readMsg()
		// md5(md5("secret"+"postgres") + "abcd")
		if pw != "md5"+md5hex(md5hex("secretpostgres")+"abcd")+"\x00" {
			_, _ = c.Write(pgErr("28P01", "password authentication failed"))
			return
		}
	case "scram":
		_, _ = c.Write(pgAuth(10, "SCRAM-SHA-256\x00\x00"))
		_, first := readMsg()
		bare := first[strings.Index(first, "n=,r="):]
		nonce := strings.TrimPrefix(bare, "n=,r=") + "srv"
		salt := []byte("saltsalt")
		serverFirst := "r=" + nonce + ",s=" + base64.StdEncoding.EncodeToString(salt) + ",i=4096"
		_, _ = c.Write(pgAuth(11, serverFirst))
		_, final := readMsg()
		withoutProof := final[:strings.Index(final, ",p=")]
		proof, _ := base64.StdEncoding.DecodeString(final[strings.Index(final, ",p=")+3:])
		salted := pbkdf2SHA256([]byte("secret"), salt, 4096)
		authMsg := bare + "," + serverFirst + "," + withoutProof
		clientKey := hmacSHA256(salted, "Client Key")
		stored := sha256.Sum256(clientKey)
		sig := hmacSHA256(stored[:], authMsg)
		for i := range proof {
			proof[i] ^= sig[i]
		}
		if got := sha256.Sum256(proof); got != stored {
			_, _ = c.Write(pgErr("28P01", "password authentication failed"))
			return
		}
		ss := hmacSHA256(hmacSHA256(salted, "Server Key"), authMsg)
		_, _ = c.Write(pgAuth(12, "v="+base64.StdEncoding.EncodeToString(ss)))
	}
	_, _ = c.Write(append(append(pgAuth(0, ""), pgMsg('S', []byte("server_version\x0017\x00"))...), pgReady('I')...))
	for {
		typ, q := readMsg()
		if typ != 'Q' {
			return
		}
		q = strings.TrimRight(q, "\x00")
		if strings.HasPrefix(q, "CREATE TABLE") {
			_, _ = c.Write(append(append(pgTag("CREATE TABLE"), pgTag("CREATE TABLE")...), pgReady('I')...))
			continue
		}
		if q == "ROLLBACK" {
			_, _ = c.Write(append(pgTag("ROLLBACK"), pgReady('I')...))
			continue
		}
		out := answer(q)
		if out == nil {
			return
		}
		_, _ = c.Write(out)
	}
}

func md5hex(s string) string {
	h := md5sum([]byte(s))
	return h
}

func pgClient(addr string) *Postgres {
	return NewPostgres(addr, "t_", ConnOptions{User: "postgres", Password: "secret"})
}

func TestPostgresAuthentication(t *testing.T) {
	ok := func(q string) []byte { return append(append(pgRow("42"), pgTag("SELECT 1")...), pgReady('I')...) }
	for _, auth := range []string{"trust", "md5", "scram"} {
		pg := pgClient(fakePG(t, auth, ok))
		out := pg.Invoke(context.Background(), history.Op{F: history.FRead, Key: "k"})
		if out.Type != history.OK || out.Value == nil || *out.Value != 42 {
			t.Fatalf("%s: %+v", auth, out)
		}
		pg.Close()
	}
	// A wrong password is a definite failure.
	pg := NewPostgres(fakePG(t, "scram", ok), "t_", ConnOptions{User: "postgres", Password: "wrong"})
	defer pg.Close()
	if out := pg.Invoke(context.Background(), history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)}); out.Type != history.Fail {
		t.Fatalf("failed authentication must be fail, got %+v", out)
	}
}

func TestPostgresAppendTransaction(t *testing.T) {
	var got string
	addr := fakePG(t, "trust", func(q string) []byte {
		got = q
		// BEGIN; read k1 (absent: zero rows); append k1; read k2; COMMIT.
		var b []byte
		b = append(b, pgTag("BEGIN")...)
		b = append(b, pgTag("SELECT 0")...)
		b = append(b, pgTag("INSERT 0 1")...)
		b = append(b, pgRow("3,5")...)
		b = append(b, pgTag("SELECT 1")...)
		b = append(b, pgTag("COMMIT")...)
		return append(b, pgReady('I')...)
	})
	pg := pgClient(addr)
	pg.opt.Isolation = "REPEATABLE READ"
	defer pg.Close()
	op := history.Op{F: history.FTxn, Txn: []history.MicroOp{
		{F: history.MicroRead, Key: "k1"}, {F: history.MicroAppend, Key: "k1", Value: history.Int(9)}, {F: history.MicroRead, Key: "k2"},
	}}
	out := pg.Invoke(context.Background(), op)
	if out.Type != history.OK {
		t.Fatalf("%+v", out)
	}
	// The empty read must not steal the next read's row.
	if out.Txn[0].Values == nil || len(out.Txn[0].Values) != 0 || len(out.Txn[2].Values) != 2 || out.Txn[2].Values[1] != 5 {
		t.Fatalf("read results misattributed: %+v", out.Txn)
	}
	for _, want := range []string{"BEGIN ISOLATION LEVEL REPEATABLE READ;", "|| ',' ||", "COMMIT;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("query lacks %q: %s", want, got)
		}
	}
}

func TestPostgresClassification(t *testing.T) {
	write := history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)}
	appendTxn := history.Op{F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroAppend, Key: "k", Value: history.Int(1)}}}
	readTxn := history.Op{F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroRead, Key: "k"}}}
	cases := []struct {
		name   string
		answer []byte
		op     history.Op
		want   history.Type
	}{
		{"serialization failure", append(pgErr("40001", "could not serialize"), pgReady('E')...), appendTxn, history.Fail},
		{"read only standby", append(pgErr("25006", "read-only transaction"), pgReady('I')...), write, history.Fail},
		{"admin shutdown", append(pgErr("57P01", "terminating connection"), pgReady('I')...), write, history.Info},
		{"connection dropped", nil, appendTxn, history.Info},
		{"dropped read", nil, readTxn, history.Fail},
		{"aborted commit", append(append(pgTag("BEGIN"), append(pgTag("INSERT 0 1"), pgTag("ROLLBACK")...)...), pgReady('I')...), appendTxn, history.Fail},
	}
	for _, c := range cases {
		answer := c.answer
		pg := pgClient(fakePG(t, "trust", func(string) []byte { return answer }))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if out := pg.Invoke(ctx, c.op); out.Type != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, out.Type, out.Error, c.want)
		}
		cancel()
		pg.Close()
	}
	// Nothing listening: the request was never sent.
	pg := pgClient("127.0.0.1:1")
	if out := pg.Invoke(context.Background(), appendTxn); out.Type != history.Fail {
		t.Fatalf("dial failure must be fail, got %s", out.Type)
	}
}

func md5sum(b []byte) string {
	h := md5.Sum(b) //nolint:gosec
	return hex.EncodeToString(h[:])
}
