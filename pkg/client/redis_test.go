package client

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

func TestReadReply(t *testing.T) {
	cases := map[string]any{
		"+OK\r\n":                 "OK",
		":42\r\n":                 int64(42),
		"$3\r\nfoo\r\n":           "foo",
		"$0\r\n\r\n":              "",
		"$-1\r\n":                 nil,
		"*2\r\n:1\r\n$1\r\nx\r\n": []any{int64(1), "x"},
		"*-1\r\n":                 nil,
	}
	for in, want := range cases {
		got, err := readReply(bufio.NewReader(strings.NewReader(in)))
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q: got %#v, %v; want %#v", in, got, err, want)
		}
	}
	if _, err := readReply(bufio.NewReader(strings.NewReader("-READONLY replica\r\n"))); err == nil || !strings.Contains(err.Error(), "READONLY") {
		t.Fatalf("error reply: %v", err)
	}
	for _, bad := range []string{"?x\r\n", ":x\r\n", "$-5\r\n", "+OK\n", "$5\r\nab"} {
		if _, err := readReply(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

// fakeServer answers each command with the next canned reply, or stalls.
func fakeServer(t *testing.T, replies ...string) (addr string, got chan []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan []string, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rd := bufio.NewReader(c)
				for {
					v, err := readReply(rd)
					if err != nil {
						return
					}
					var args []string
					for _, a := range v.([]any) {
						args = append(args, a.(string))
					}
					got <- args
					if len(replies) == 0 {
						time.Sleep(time.Second) // never answer
						return
					}
					r := replies[0]
					replies = replies[1:]
					if r == "" {
						return // drop the connection after receiving
					}
					c.Write([]byte(r))
				}
			}(c)
		}
	}()
	return ln.Addr().String(), got
}

func TestRedisProtocolAndClassification(t *testing.T) {
	ctx := context.Background()
	inv := func(f string, v, old *int64) history.Op {
		return history.Op{Process: 1, Type: history.Invoke, F: f, Key: "k", Value: v, Old: old}
	}
	addr, got := fakeServer(t, "$1\r\n3\r\n", "+OK\r\n", ":1\r\n", "-READONLY You can't write against a read only replica.\r\n")
	r := NewRedis(addr, "p:")
	defer r.Close()

	if out := r.Invoke(ctx, inv(history.FRead, nil, nil)); out.Type != history.OK || *out.Value != 3 {
		t.Fatalf("read: %+v", out)
	}
	if args := <-got; strings.Join(args, " ") != "GET p:k" {
		t.Fatalf("sent %v", args)
	}
	if out := r.Invoke(ctx, inv(history.FWrite, history.Int(7), nil)); out.Type != history.OK {
		t.Fatalf("write: %+v", out)
	}
	if args := <-got; strings.Join(args, " ") != "SET p:k 7" {
		t.Fatalf("sent %v", args)
	}
	if out := r.Invoke(ctx, inv(history.FCas, history.Int(2), history.Int(1))); out.Type != history.OK {
		t.Fatalf("cas ok: %+v", out)
	}
	if args := <-got; len(args) != 6 || args[0] != "EVAL" || args[2] != "1" || args[3] != "p:k" || args[4] != "1" || args[5] != "2" {
		t.Fatalf("sent %v", args)
	}
	// An error reply means the server rejected the command: definitely not
	// applied, and the connection stays usable.
	if out := r.Invoke(ctx, inv(history.FWrite, history.Int(9), nil)); out.Type != history.Fail || !strings.Contains(out.Error, "READONLY") {
		t.Fatalf("a rejected write is a definite failure: %+v", out)
	}
	if r.conn == nil {
		t.Fatal("an error reply must not drop the connection")
	}
}

func TestRedisCasMismatchIsFail(t *testing.T) {
	addr, _ := fakeServer(t, ":0\r\n", "$-1\r\n")
	r := NewRedis(addr, "")
	defer r.Close()
	out := r.Invoke(context.Background(), history.Op{F: history.FCas, Key: "k", Old: history.Int(1), Value: history.Int(2)})
	if out.Type != history.Fail {
		t.Fatalf("cas that did not match: %+v", out)
	}
	out = r.Invoke(context.Background(), history.Op{F: history.FRead, Key: "k"})
	if out.Type != history.OK || out.Value != nil {
		t.Fatalf("read of a missing key: %+v", out)
	}
}

func TestRedisAmbiguousFailures(t *testing.T) {
	// The server receives the write and then drops the connection: the write
	// may have been applied, so it must be "info"...
	addr, _ := fakeServer(t, "")
	r := NewRedis(addr, "")
	out := r.Invoke(context.Background(), history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)})
	if out.Type != history.Info {
		t.Fatalf("dropped after send: %+v", out)
	}
	if r.conn != nil {
		t.Fatal("a broken connection must be discarded")
	}
	// ...a timeout likewise, while a read that times out is simply "fail".
	addr, _ = fakeServer(t)
	r = NewRedis(addr, "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if out := r.Invoke(ctx, history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)}); out.Type != history.Info {
		t.Fatalf("timed out write: %+v", out)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if out := r.Invoke(ctx2, history.Op{F: history.FRead, Key: "k"}); out.Type != history.Fail {
		t.Fatalf("timed out read: %+v", out)
	}
	// Nothing listening: the request never left, so even a write is "fail".
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	if out := NewRedis(dead, "").Invoke(context.Background(), history.Op{F: history.FWrite, Key: "k", Value: history.Int(1)}); out.Type != history.Fail {
		t.Fatalf("connection refused: %+v", out)
	}
}

// TestRealRedis exercises the client against a real redis-server or
// valkey-server when one is installed.
func TestRealRedis(t *testing.T) {
	bin := ""
	for _, b := range []string{"valkey-server", "redis-server"} {
		if p, err := exec.LookPath(b); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		t.Skip("no redis-server or valkey-server installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(bin, "--port", strconv.Itoa(port), "--save", "", "--appendonly", "no", "--bind", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	r := NewRedis(addr, fmt.Sprintf("strife-test-%d:", time.Now().UnixNano()))
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if out := r.Invoke(ctx, history.Op{F: history.FRead, Key: "k"}); out.Type == history.OK {
			if out.Value != nil {
				t.Fatalf("fresh key should be absent: %+v", out)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatal("server never came up")
		}
		time.Sleep(50 * time.Millisecond)
	}
	steps := []struct {
		op   history.Op
		want history.Type
		val  *int64
	}{
		{history.Op{F: history.FWrite, Key: "k", Value: history.Int(3)}, history.OK, nil},
		{history.Op{F: history.FRead, Key: "k"}, history.OK, history.Int(3)},
		{history.Op{F: history.FCas, Key: "k", Old: history.Int(1), Value: history.Int(4)}, history.Fail, nil},
		{history.Op{F: history.FCas, Key: "k", Old: history.Int(3), Value: history.Int(4)}, history.OK, nil},
		{history.Op{F: history.FRead, Key: "k"}, history.OK, history.Int(4)},
		{history.Op{F: history.FCas, Key: "absent", Old: history.Int(0), Value: history.Int(1)}, history.Fail, nil},
	}
	for i, s := range steps {
		out := r.Invoke(ctx, s.op)
		if out.Type != s.want || (s.val != nil && (out.Value == nil || *out.Value != *s.val)) {
			t.Fatalf("step %d %s: got %+v", i, s.op.F, out)
		}
	}
}
