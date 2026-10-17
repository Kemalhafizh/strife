package client

import (
	"bufio"
	"context"
	"crypto/md5" //nolint:gosec // PostgreSQL's legacy password protocol
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Postgres speaks the PostgreSQL frontend/backend protocol 3.0 (simple query
// flow) with the standard library only. It authenticates with trust,
// cleartext, MD5 or SCRAM-SHA-256 (the default since PostgreSQL 14) and can
// encrypt the connection with TLS.
//
// Operations:
//   - read / write / cas / add on single keys (table <prefix>kv);
//   - txn: list-append transactions (table <prefix>lists, one row per key
//     holding the comma separated list), run as one simple query
//     "BEGIN ISOLATION LEVEL ...; ...; COMMIT;".
//
// Classification of a failed operation:
//   - read-only operations: always fail (nothing changed);
//   - the server answered with an error the statement could not survive
//     (serialization failure 40001, deadlock 40P01, constraint 23xxx,
//     read-only 25006, syntax/data errors...): fail, the transaction was
//     rolled back;
//   - connection lost, timeout, or an error that can arrive after a commit
//     (admin/crash shutdown 57P0x, connection exceptions 08xxx, resources
//     53xxx, internal XX...): info, the outcome is unknown;
//   - errors before the request was sent: fail.
type Postgres struct {
	addr, prefix string
	opt          ConnOptions
	conn         net.Conn
	rd           *bufio.Reader
	schema       bool
}

// NewPostgres returns a client for one node ("10.0.0.1:5432").
func NewPostgres(addr, prefix string, opt ConnOptions) *Postgres {
	if opt.User == "" {
		opt.User = "postgres"
	}
	if opt.Database == "" {
		opt.Database = opt.User
	}
	if opt.Isolation == "" {
		opt.Isolation = "SERIALIZABLE"
	}
	return &Postgres{addr: addr, prefix: prefix, opt: opt}
}

// Close implements Client.
func (p *Postgres) Close() error {
	if p.conn == nil {
		return nil
	}
	_ = p.writeMsg('X', nil)
	err := p.conn.Close()
	p.conn, p.rd = nil, nil
	return err
}

func (p *Postgres) drop() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn, p.rd = nil, nil
}

// postgresError is an ErrorResponse from the server.
type postgresError struct {
	code string // SQLSTATE
	msg  string
}

func (e *postgresError) Error() string { return fmt.Sprintf("postgres: [%s] %s", e.code, e.msg) }

// ambiguous reports whether the error leaves the outcome of a write unknown.
func (e *postgresError) ambiguous() bool {
	switch {
	case strings.HasPrefix(e.code, "08"), // connection exception
		strings.HasPrefix(e.code, "57P"), // admin/crash shutdown, idle timeout
		strings.HasPrefix(e.code, "53"),  // insufficient resources
		strings.HasPrefix(e.code, "58"),  // system error
		strings.HasPrefix(e.code, "XX"),  // internal error
		e.code == "":
		return true
	}
	return false
}

func (p *Postgres) writeMsg(typ byte, body []byte) error {
	var hdr []byte
	if typ != 0 {
		hdr = append(hdr, typ)
	}
	hdr = binary.BigEndian.AppendUint32(hdr, uint32(len(body)+4))
	_, err := p.conn.Write(append(hdr, body...))
	return err
}

func (p *Postgres) readMsg() (byte, []byte, error) {
	typ, err := p.rd.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	var n uint32
	if err := binary.Read(p.rd, binary.BigEndian, &n); err != nil {
		return 0, nil, err
	}
	if n < 4 || n > 1<<30 {
		return 0, nil, fmt.Errorf("postgres: bad message length %d", n)
	}
	body, err := readN(p.rd, int(n-4))
	return typ, body, err
}

func (p *Postgres) dial(ctx context.Context) error {
	if p.conn != nil {
		return nil
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	p.conn, p.rd = c, bufio.NewReader(c)
	if err := p.handshake(ctx); err != nil {
		p.drop()
		return err
	}
	if p.opt.InitSQL != "" {
		if _, err := p.exec(ctx, p.opt.InitSQL); err != nil {
			p.drop()
			return fmt.Errorf("postgres: init_sql: %w", err)
		}
	}
	return nil
}

func (p *Postgres) handshake(ctx context.Context) error {
	tcfg, err := p.opt.tlsConfig(p.addr)
	if err != nil {
		return err
	}
	if tcfg != nil {
		// SSLRequest: length 8, code 80877103.
		if err := p.writeMsg(0, binary.BigEndian.AppendUint32(nil, 80877103)); err != nil {
			return err
		}
		answer, err := p.rd.ReadByte()
		if err != nil {
			return err
		}
		if answer != 'S' {
			return fmt.Errorf("postgres: server %s refused TLS (db.tls=%s)", p.addr, p.opt.TLS)
		}
		tc := tls.Client(p.conn, tcfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("postgres: TLS: %w", err)
		}
		p.conn, p.rd = tc, bufio.NewReader(tc)
	}
	var startup []byte
	startup = binary.BigEndian.AppendUint32(startup, 196608) // protocol 3.0
	for _, kv := range [][2]string{{"user", p.opt.User}, {"database", p.opt.Database}, {"application_name", "strife"}} {
		startup = append(append(append(append(startup, kv[0]...), 0), kv[1]...), 0)
	}
	if err := p.writeMsg(0, append(startup, 0)); err != nil {
		return err
	}
	var sc *scram
	for {
		typ, body, err := p.readMsg()
		if err != nil {
			return err
		}
		switch typ {
		case 'E':
			return parseErrorResponse(body)
		case 'Z':
			return nil
		case 'R':
			if len(body) < 4 {
				return fmt.Errorf("postgres: short authentication message")
			}
			switch code := binary.BigEndian.Uint32(body); code {
			case 0: // AuthenticationOk
			case 3: // cleartext password
				if err := p.writeMsg('p', append([]byte(p.opt.Password), 0)); err != nil {
					return err
				}
			case 5: // MD5: "md5" + md5(md5(password + user) + salt)
				inner := md5.Sum([]byte(p.opt.Password + p.opt.User)) //nolint:gosec
				outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), body[4:8]...))
				if err := p.writeMsg('p', append([]byte("md5"+hex.EncodeToString(outer[:])), 0)); err != nil {
					return err
				}
			case 10: // SASL: list of mechanisms
				if !strings.Contains(string(body[4:]), "SCRAM-SHA-256\x00") {
					return fmt.Errorf("postgres: no supported SASL mechanism in %q", body[4:])
				}
				if sc, err = newSCRAM(p.opt.Password); err != nil {
					return err
				}
				first := sc.first()
				msg := append([]byte("SCRAM-SHA-256\x00"), binary.BigEndian.AppendUint32(nil, uint32(len(first)))...)
				if err := p.writeMsg('p', append(msg, first...)); err != nil {
					return err
				}
			case 11: // SASLContinue
				if sc == nil {
					return fmt.Errorf("postgres: unexpected SASL continue")
				}
				final, err := sc.final(string(body[4:]))
				if err != nil {
					return err
				}
				if err := p.writeMsg('p', []byte(final)); err != nil {
					return err
				}
			case 12: // SASLFinal
				if sc == nil {
					return fmt.Errorf("postgres: unexpected SASL final")
				}
				if err := sc.verify(string(body[4:])); err != nil {
					return err
				}
			default:
				return fmt.Errorf("postgres: unsupported authentication method %d", code)
			}
		}
	}
}

// stmtResult is the outcome of one statement of a simple query.
type stmtResult struct {
	tag  string
	rows [][]*string // nil column = NULL
}

// notSent marks an error that happened before the request left this client.
type notSent struct{ err error }

func (e *notSent) Error() string { return e.err.Error() }
func (e *notSent) Unwrap() error { return e.err }

// exec runs a simple query and returns one result per statement. A server
// error ends the query (later statements do not run); an explicit
// transaction left open by it is rolled back.
func (p *Postgres) exec(ctx context.Context, sql string) ([]stmtResult, error) {
	if err := p.dial(ctx); err != nil {
		return nil, &notSent{err}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = p.conn.SetDeadline(dl)
	} else {
		_ = p.conn.SetDeadline(time.Time{})
	}
	if err := p.writeMsg('Q', append([]byte(sql), 0)); err != nil {
		p.drop()
		return nil, &sentError{err}
	}
	var (
		results []stmtResult
		cur     stmtResult
		qerr    error
	)
	for {
		typ, body, err := p.readMsg()
		if err != nil {
			p.drop()
			return nil, &sentError{err}
		}
		switch typ {
		case 'D':
			row, err := parseDataRow(body)
			if err != nil {
				p.drop()
				return nil, &sentError{err}
			}
			cur.rows = append(cur.rows, row)
		case 'C':
			cur.tag = strings.TrimRight(string(body), "\x00")
			results = append(results, cur)
			cur = stmtResult{}
		case 'I': // EmptyQueryResponse
			results = append(results, stmtResult{})
		case 'E':
			qerr = parseErrorResponse(body)
		case 'Z':
			if len(body) == 1 && body[0] != 'I' {
				// Still inside a (failed) transaction block.
				if _, err := p.execRaw("ROLLBACK"); err != nil {
					p.drop()
				}
			}
			if qerr != nil {
				return results, qerr
			}
			return results, nil
		}
	}
}

// execRaw runs a statement whose results do not matter.
func (p *Postgres) execRaw(sql string) (byte, error) {
	if err := p.writeMsg('Q', append([]byte(sql), 0)); err != nil {
		return 0, err
	}
	for {
		typ, body, err := p.readMsg()
		if err != nil {
			return 0, err
		}
		if typ == 'Z' && len(body) == 1 {
			return body[0], nil
		}
	}
}

func parseDataRow(body []byte) ([]*string, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("postgres: short DataRow")
	}
	n := int(binary.BigEndian.Uint16(body))
	off := 2
	row := make([]*string, 0, n)
	for i := 0; i < n; i++ {
		if off+4 > len(body) {
			return nil, fmt.Errorf("postgres: truncated DataRow")
		}
		l := int32(binary.BigEndian.Uint32(body[off:]))
		off += 4
		if l < 0 {
			row = append(row, nil)
			continue
		}
		if off+int(l) > len(body) {
			return nil, fmt.Errorf("postgres: truncated DataRow column")
		}
		v := string(body[off : off+int(l)])
		off += int(l)
		row = append(row, &v)
	}
	return row, nil
}

func parseErrorResponse(body []byte) error {
	e := &postgresError{}
	for len(body) > 0 && body[0] != 0 {
		field := body[0]
		end := 1
		for end < len(body) && body[end] != 0 {
			end++
		}
		val := string(body[1:end])
		switch field {
		case 'C':
			e.code = val
		case 'M':
			e.msg = val
		}
		if end >= len(body) {
			break
		}
		body = body[end+1:]
	}
	return e
}

func (p *Postgres) tables() (kv, lists string) {
	return sanitizeTable(p.prefix + "kv"), sanitizeTable(p.prefix + "lists")
}

func (p *Postgres) ensureSchema(ctx context.Context) error {
	if p.schema {
		return nil
	}
	kv, lists := p.tables()
	sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (k TEXT PRIMARY KEY, v BIGINT); CREATE TABLE IF NOT EXISTS %s (k TEXT PRIMARY KEY, v TEXT NOT NULL);", kv, lists)
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if _, err = p.exec(ctx, sql); err == nil {
			p.schema = true
			return nil
		}
		// Concurrent CREATE TABLE IF NOT EXISTS can still collide in the
		// catalog (23505 / 42P07); the table exists afterwards.
		var pe *postgresError
		if !errors.As(err, &pe) || (pe.code != "23505" && pe.code != "42P07") {
			break
		}
	}
	return err
}

func sanitizeTable(name string) string {
	var b strings.Builder
	b.WriteString("tbl_")
	for _, ch := range strings.ToLower(name) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			b.WriteRune(ch)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func escapeSQL(s string) string { return strings.ReplaceAll(s, "'", "''") }

// Invoke implements Client.
func (p *Postgres) Invoke(ctx context.Context, op history.Op) history.Op {
	if err := p.ensureSchema(ctx); err != nil {
		// The operation itself never ran.
		out := complete(op, history.Fail)
		out.Error = err.Error()
		return out
	}
	out, err := p.invoke(ctx, op)
	if err == nil {
		return out
	}
	out = complete(op, sqlOutcome(op, err))
	out.Error = err.Error()
	return out
}

// sqlOutcome classifies a failed SQL operation (shared by PostgreSQL and
// MySQL).
func sqlOutcome(op history.Op, err error) history.Type {
	if readOnly(op) {
		return history.Fail
	}
	var ns *notSent
	if errors.As(err, &ns) {
		return history.Fail
	}
	type ambiguousErr interface{ ambiguous() bool }
	var ae ambiguousErr
	if errors.As(err, &ae) && !ae.ambiguous() {
		return history.Fail
	}
	return history.Info
}

// readOnly reports whether op cannot change the database.
func readOnly(op history.Op) bool {
	if op.F == history.FRead {
		return true
	}
	if op.F != history.FTxn {
		return false
	}
	for _, m := range op.Txn {
		if m.F != history.MicroRead {
			return false
		}
	}
	return true
}

func (p *Postgres) invoke(ctx context.Context, op history.Op) (history.Op, error) {
	kv, lists := p.tables()
	key := escapeSQL(op.Key)
	switch op.F {
	case history.FRead:
		res, err := p.exec(ctx, fmt.Sprintf("SELECT v FROM %s WHERE k = '%s'", kv, key))
		if err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		if len(res) == 1 && len(res[0].rows) == 1 && res[0].rows[0][0] != nil {
			n, err := strconv.ParseInt(*res[0].rows[0][0], 10, 64)
			if err != nil {
				return history.Op{}, &notSent{fmt.Errorf("postgres: non-integer value %q", *res[0].rows[0][0])}
			}
			out.Value = history.Int(n)
		}
		return out, nil
	case history.FWrite:
		_, err := p.exec(ctx, fmt.Sprintf("INSERT INTO %s (k, v) VALUES ('%s', %d) ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v", kv, key, *op.Value))
		if err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil
	case history.FCas:
		res, err := p.exec(ctx, fmt.Sprintf("UPDATE %s SET v = %d WHERE k = '%s' AND v = %d", kv, *op.Value, key, *op.Old))
		if err != nil {
			return history.Op{}, err
		}
		if len(res) == 1 && res[0].tag == "UPDATE 1" {
			return complete(op, history.OK), nil
		}
		return complete(op, history.Fail), nil
	case history.FAdd, history.FInc:
		delta := int64(1)
		if op.Value != nil {
			delta = *op.Value
		}
		_, err := p.exec(ctx, fmt.Sprintf("INSERT INTO %[1]s (k, v) VALUES ('%[2]s', %[3]d) ON CONFLICT (k) DO UPDATE SET v = %[1]s.v + %[3]d", kv, key, delta))
		if err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil
	case history.FTxn:
		return p.txn(ctx, op, lists)
	}
	return history.Op{}, &notSent{fmt.Errorf("postgres: unsupported operation %q", op.F)}
}

// txn runs a list-append transaction as a single simple query, so that a
// lost connection can only happen before BEGIN reached the server or while
// the outcome is genuinely unknown.
func (p *Postgres) txn(ctx context.Context, op history.Op, lists string) (history.Op, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "BEGIN ISOLATION LEVEL %s;", p.opt.Isolation)
	for _, m := range op.Txn {
		k := escapeSQL(m.Key)
		switch m.F {
		case history.MicroAppend:
			fmt.Fprintf(&b, " INSERT INTO %[1]s (k, v) VALUES ('%[2]s', '%[3]d') ON CONFLICT (k) DO UPDATE SET v = %[1]s.v || ',' || EXCLUDED.v;", lists, k, *m.Value)
		case history.MicroRead:
			fmt.Fprintf(&b, " SELECT v FROM %s WHERE k = '%s';", lists, k)
		default:
			return history.Op{}, &notSent{fmt.Errorf("postgres: unsupported micro-op %q", m.F)}
		}
	}
	b.WriteString(" COMMIT;")
	res, err := p.exec(ctx, b.String())
	if err != nil {
		return history.Op{}, err
	}
	if len(res) != len(op.Txn)+2 || res[len(res)-1].tag != "COMMIT" {
		// COMMIT of an aborted transaction answers ROLLBACK.
		return history.Op{}, &postgresError{code: "40000", msg: fmt.Sprintf("transaction did not commit (%d results, last %q)", len(res), res[len(res)-1].tag)}
	}
	out := complete(op, history.OK)
	for i, m := range op.Txn {
		if m.F != history.MicroRead {
			continue
		}
		vals := []int64{}
		if r := res[i+1]; len(r.rows) == 1 && r.rows[0][0] != nil {
			var err error
			if vals, err = parseList(*r.rows[0][0]); err != nil {
				return history.Op{}, &postgresError{code: "XX000", msg: err.Error()}
			}
		}
		out.Txn[i].Values = vals
	}
	return out, nil
}

// parseList parses "1,2,3".
func parseList(s string) ([]int64, error) {
	if s == "" {
		return []int64{}, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("list element %q: %w", p, err)
		}
		out[i] = v
	}
	return out, nil
}
