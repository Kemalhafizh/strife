package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Redis speaks RESP2 to Redis or Valkey with the standard library only.
//
//   - read:  GET key
//   - write: SET key v
//   - cas:   an EVAL script that compares and sets atomically on the server
//
// Each client owns one connection. Any I/O error closes it: a reply that
// arrives late on a reused connection would be attributed to the next
// command, which is exactly the kind of client bug that fakes anomalies.
type Redis struct {
	addr   string
	prefix string
	opt    ConnOptions
	conn   net.Conn
	rd     *bufio.Reader
}

// casScript returns 1 if the key held ARGV[1] and was set to ARGV[2], else 0.
const casScript = `local v = redis.call('GET', KEYS[1])
if v == ARGV[1] then redis.call('SET', KEYS[1], ARGV[2]) return 1 end
return 0`

// NewRedis returns a client for one server, e.g. "10.0.0.1:6379".
func NewRedis(addr, prefix string) *Redis { return &Redis{addr: addr, prefix: prefix} }

// NewRedisWith returns a client that authenticates (AUTH [user] password)
// and optionally encrypts the connection.
func NewRedisWith(addr, prefix string, opt ConnOptions) *Redis {
	return &Redis{addr: addr, prefix: prefix, opt: opt}
}

// Close implements Client.
func (r *Redis) Close() error {
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn, r.rd = nil, nil
	return err
}

// redisError is an error reply from the server ("-ERR ...", "-READONLY ...").
// The server rejected the command: it was not executed.
type redisError struct{ msg string }

func (e *redisError) Error() string { return "redis: " + e.msg }

// sentError wraps failures that happened after the request may have reached
// the server; before that, failures are definite.
type sentError struct{ err error }

func (e *sentError) Error() string { return e.err.Error() }
func (e *sentError) Unwrap() error { return e.err }

func (r *Redis) dial(ctx context.Context) error {
	if r.conn != nil {
		return nil
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return err
	}
	tcfg, err := r.opt.tlsConfig(r.addr)
	if err != nil {
		_ = c.Close()
		return err
	}
	if tcfg != nil {
		tc := tls.Client(c, tcfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = c.Close()
			return fmt.Errorf("redis: TLS: %w", err)
		}
		c = tc
	}
	r.conn, r.rd = c, bufio.NewReader(c)
	if r.opt.Password != "" {
		args := []string{"AUTH", r.opt.Password}
		if r.opt.User != "" {
			args = []string{"AUTH", r.opt.User, r.opt.Password}
		}
		if _, err := r.do(ctx, args...); err != nil {
			r.Close()
			// AUTH changes nothing: whatever failed, no operation ran.
			return fmt.Errorf("redis: AUTH: %v", err)
		}
	}
	return nil
}

// do sends one command and reads one reply.
func (r *Redis) do(ctx context.Context, args ...string) (any, error) {
	if err := r.dial(ctx); err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = r.conn.SetDeadline(dl)
	} else {
		_ = r.conn.SetDeadline(time.Time{})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(r.conn, b.String()); err != nil {
		r.Close()
		// A partial write may still have delivered a complete command.
		return nil, &sentError{err}
	}
	v, err := readReply(r.rd)
	if err != nil {
		var re *redisError
		if !errors.As(err, &re) {
			r.Close()
			return nil, &sentError{err}
		}
		return nil, err
	}
	return v, nil
}

// readReply parses one RESP2 value: string, error, integer, bulk (nil-able)
// or array.
func readReply(rd *bufio.Reader) (any, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || !strings.HasSuffix(line, "\r\n") {
		return nil, fmt.Errorf("redis: malformed reply %q", line)
	}
	body := line[1 : len(line)-2]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, &redisError{body}
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("redis: bad integer %q", body)
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 512<<20 {
			return nil, fmt.Errorf("redis: bad bulk length %q", body)
		}
		if n == -1 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(rd, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 1<<20 {
			return nil, fmt.Errorf("redis: bad array length %q", body)
		}
		if n == -1 {
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = readReply(rd); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("redis: unknown reply type %q", line[0])
}

// Invoke implements Client.
func (r *Redis) Invoke(ctx context.Context, op history.Op) history.Op {
	out, err := r.invoke(ctx, op)
	if err == nil {
		return out
	}
	t := history.Info
	var re *redisError
	var se *sentError
	switch {
	case readOnly(op):
		t = history.Fail // reads have no effect
	case errors.As(err, &re):
		t = history.Fail // e.g. READONLY on a replica: rejected, not executed
	case !errors.As(err, &se):
		t = history.Fail // never left the client (dial failed)
	}
	out = complete(op, t)
	out.Error = err.Error()
	return out
}

func (r *Redis) invoke(ctx context.Context, op history.Op) (history.Op, error) {
	key := r.prefix + op.Key
	switch op.F {
	case history.FRead:
		v, err := r.do(ctx, "GET", key)
		if err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		if v != nil {
			s, ok := v.(string)
			if !ok {
				return history.Op{}, fmt.Errorf("redis: GET returned %T", v)
			}
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return history.Op{}, fmt.Errorf("redis: non-integer value %q", s)
			}
			out.Value = history.Int(n)
		}
		return out, nil
	case history.FWrite:
		v, err := r.do(ctx, "SET", key, strconv.FormatInt(*op.Value, 10))
		if err != nil {
			return history.Op{}, err
		}
		if v != "OK" {
			return history.Op{}, &sentError{fmt.Errorf("redis: SET replied %v", v)}
		}
		return complete(op, history.OK), nil
	case history.FCas:
		v, err := r.do(ctx, "EVAL", casScript, "1", key, strconv.FormatInt(*op.Old, 10), strconv.FormatInt(*op.Value, 10))
		if err != nil {
			return history.Op{}, err
		}
		switch v {
		case int64(1):
			return complete(op, history.OK), nil
		case int64(0):
			return complete(op, history.Fail), nil
		}
		return history.Op{}, &sentError{fmt.Errorf("redis: cas script replied %v", v)}
	case history.FTxn:
		return r.txn(ctx, op)
	}
	return history.Op{}, &redisError{"unsupported op " + op.F}
}

// appendScript runs a list-append transaction atomically: KEYS[i] with
// ARGV[2i-1] = "a" (RPUSH ARGV[2i]) or "r" (LRANGE), returning the reads.
const appendScript = `local out = {}
for i = 1, #KEYS do
  if ARGV[2*i-1] == 'a' then
    redis.call('RPUSH', KEYS[i], ARGV[2*i])
  else
    out[#out+1] = redis.call('LRANGE', KEYS[i], 0, -1)
  end
end
return out`

func (r *Redis) txn(ctx context.Context, op history.Op) (history.Op, error) {
	args := []string{"EVAL", appendScript, strconv.Itoa(len(op.Txn))}
	var argv []string
	for _, m := range op.Txn {
		args = append(args, r.prefix+m.Key)
		switch m.F {
		case history.MicroAppend:
			argv = append(argv, "a", strconv.FormatInt(*m.Value, 10))
		case history.MicroRead:
			argv = append(argv, "r", "")
		default:
			return history.Op{}, &redisError{"unsupported micro-op " + m.F}
		}
	}
	v, err := r.do(ctx, append(args, argv...)...)
	if err != nil {
		return history.Op{}, err
	}
	reads, ok := v.([]any)
	if !ok {
		return history.Op{}, &sentError{fmt.Errorf("redis: append script replied %T", v)}
	}
	out := complete(op, history.OK)
	j := 0
	for i, m := range op.Txn {
		if m.F != history.MicroRead {
			continue
		}
		if j >= len(reads) {
			return history.Op{}, &sentError{fmt.Errorf("redis: append script returned %d reads", len(reads))}
		}
		list, _ := reads[j].([]any)
		j++
		vals := make([]int64, 0, len(list))
		for _, e := range list {
			s, _ := e.(string)
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return history.Op{}, &sentError{fmt.Errorf("redis: list element %q", s)}
			}
			vals = append(vals, n)
		}
		out.Txn[i].Values = vals
	}
	return out, nil
}
