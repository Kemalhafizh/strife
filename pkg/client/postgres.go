package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Postgres speaks the PostgreSQL Frontend/Backend Protocol 3.0 to Postgres,
// CockroachDB, or TiDB using the standard library only.
//
// Operations supported:
//   - read:  SELECT v FROM <table> WHERE k = '<key>';
//   - write: INSERT INTO <table> (k, v) VALUES ('<key>', <v>) ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v;
//   - cas:   UPDATE <table> SET v = <new> WHERE k = '<key>' AND v = <old>;
//   - txn:   atomic multi-operation transaction (BEGIN ... micro-ops ... COMMIT);
//   - enqueue: INSERT INTO <queue_table> (q, v) VALUES ('<key>', <v>);
//   - dequeue: DELETE FROM <queue_table> WHERE id = (SELECT id FROM <queue_table> WHERE q = '<key>' ORDER BY id ASC LIMIT 1) RETURNING v;
//   - inc/add: INSERT INTO <table> (k, v) VALUES ('<key>', <delta>) ON CONFLICT (k) DO UPDATE SET v = <table>.v + <delta>;
//
// Error classification:
//   - SQLSTATE 40001 (serialization_failure): definite fail (rolled back by database engine);
//   - SQLSTATE 40P01 (deadlock_detected): definite fail;
//   - SQLSTATE 23505 (unique_violation): definite fail;
//   - Syntax or read-only server error: definite fail;
//   - Connection failure before query sent: definite fail;
//   - Timeout or connection drop on write/commit after sending: info (outcome unknown);
//   - Reads: always safe to call fail.
type Postgres struct {
	addr     string
	prefix   string
	user     string
	database string
	conn     net.Conn
	rd       *bufio.Reader
	initDone bool
}

// NewPostgres returns a PostgreSQL client for one node address (e.g. "127.0.0.1:5432").
func NewPostgres(addr, prefix string) *Postgres {
	return &Postgres{
		addr:     addr,
		prefix:   prefix,
		user:     "postgres",
		database: "postgres",
	}
}

// Close implements Client.
func (p *Postgres) Close() error {
	if p.conn == nil {
		return nil
	}
	// Try sending terminate message 'X' (length 4)
	_ = binary.Write(p.conn, binary.BigEndian, uint8('X'))
	_ = binary.Write(p.conn, binary.BigEndian, int32(4))
	err := p.conn.Close()
	p.conn, p.rd = nil, nil
	return err
}

type postgresError struct {
	code string // SQLSTATE code, e.g. "40001", "23505"
	msg  string
}

func (e *postgresError) Error() string {
	return fmt.Sprintf("postgres: [%s] %s", e.code, e.msg)
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
	p.conn, p.rd = c, bufio.NewReader(c)

	// Send StartupMessage: length(int32), protocol(int32 0x00030000), "user\0postgres\0database\0postgres\0\0"
	var payload []byte
	payload = binary.BigEndian.AppendUint32(payload, 0x00030000) // version 3.0
	payload = append(payload, "user\x00"+p.user+"\x00"...)
	payload = append(payload, "database\x00"+p.database+"\x00"...)
	payload = append(payload, "application_name\x00strife\x00"...)
	payload = append(payload, 0) // terminating null

	msgLen := int32(len(payload) + 4)
	if err := binary.Write(p.conn, binary.BigEndian, msgLen); err != nil {
		p.Close()
		return err
	}
	if _, err := p.conn.Write(payload); err != nil {
		p.Close()
		return err
	}

	// Read startup handshake responses until ReadyForQuery ('Z')
	for {
		msgType, err := p.rd.ReadByte()
		if err != nil {
			p.Close()
			return err
		}
		var length int32
		if err := binary.Read(p.rd, binary.BigEndian, &length); err != nil {
			p.Close()
			return err
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(p.rd, body); err != nil {
			p.Close()
			return err
		}

		switch msgType {
		case 'R': // Authentication request
			if len(body) >= 4 {
				authType := binary.BigEndian.Uint32(body[:4])
				if authType != 0 {
					p.Close()
					return fmt.Errorf("postgres: unsupported authentication type %d (strife expects trust/auth-ok)", authType)
				}
			}
		case 'E': // ErrorResponse
			p.Close()
			return parseErrorResponse(body)
		case 'Z': // ReadyForQuery
			return nil
		}
	}
}

// queryResult holds the output rows and command tag from a Simple Query.
type queryResult struct {
	tag  string
	rows [][]string
}

// exec sends a Simple Query ('Q') and reads all messages until ReadyForQuery ('Z').
func (p *Postgres) exec(ctx context.Context, sql string) (*queryResult, error) {
	if err := p.dial(ctx); err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = p.conn.SetDeadline(dl)
	} else {
		_ = p.conn.SetDeadline(time.Time{})
	}

	// Send 'Q' (Simple Query)
	sqlBytes := append([]byte(sql), 0)
	msgLen := int32(len(sqlBytes) + 4)
	var header [5]byte
	header[0] = 'Q'
	binary.BigEndian.PutUint32(header[1:], uint32(msgLen))

	if _, err := p.conn.Write(header[:]); err != nil {
		p.Close()
		return nil, &sentError{err}
	}
	if _, err := p.conn.Write(sqlBytes); err != nil {
		p.Close()
		return nil, &sentError{err}
	}

	res := &queryResult{}
	var queryErr error

	for {
		msgType, err := p.rd.ReadByte()
		if err != nil {
			p.Close()
			if queryErr != nil {
				return nil, queryErr
			}
			return nil, &sentError{err}
		}
		var length int32
		if err := binary.Read(p.rd, binary.BigEndian, &length); err != nil {
			p.Close()
			return nil, &sentError{err}
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(p.rd, body); err != nil {
			p.Close()
			return nil, &sentError{err}
		}

		switch msgType {
		case 'D': // DataRow
			if len(body) >= 2 {
				numCols := binary.BigEndian.Uint16(body[:2])
				offset := 2
				var row []string
				for i := 0; i < int(numCols); i++ {
					if offset+4 > len(body) {
						break
					}
					colLen := int32(binary.BigEndian.Uint32(body[offset : offset+4]))
					offset += 4
					if colLen == -1 {
						row = append(row, "") // null
					} else {
						val := string(body[offset : offset+int(colLen)])
						offset += int(colLen)
						row = append(row, val)
					}
				}
				res.rows = append(res.rows, row)
			}
		case 'C': // CommandComplete
			tag := strings.TrimRight(string(body), "\x00")
			res.tag = tag
		case 'E': // ErrorResponse
			queryErr = parseErrorResponse(body)
		case 'Z': // ReadyForQuery
			if queryErr != nil {
				return nil, queryErr
			}
			return res, nil
		}
	}
}

func parseErrorResponse(body []byte) error {
	code := ""
	msg := ""
	idx := 0
	for idx < len(body) {
		field := body[idx]
		idx++
		if field == 0 {
			break
		}
		end := idx
		for end < len(body) && body[end] != 0 {
			end++
		}
		val := string(body[idx:end])
		idx = end + 1
		switch field {
		case 'C':
			code = val
		case 'M':
			msg = val
		}
	}
	return &postgresError{code: code, msg: msg}
}

func (p *Postgres) ensureSchema(ctx context.Context) error {
	if p.initDone {
		return nil
	}
	tbl := sanitizeTable(p.prefix + "kv")
	qTbl := sanitizeTable(p.prefix + "queue")
	schemaSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (k TEXT PRIMARY KEY, v BIGINT);
		CREATE TABLE IF NOT EXISTS %s (id BIGSERIAL PRIMARY KEY, q TEXT, v BIGINT);
	`, tbl, qTbl)
	_, err := p.exec(ctx, schemaSQL)
	if err == nil {
		p.initDone = true
	}
	return err
}

func sanitizeTable(name string) string {
	var b strings.Builder
	b.WriteString("tbl_")
	for _, ch := range name {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			b.WriteRune(ch)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Invoke implements Client.
func (p *Postgres) Invoke(ctx context.Context, op history.Op) history.Op {
	out, err := p.invoke(ctx, op)
	if err == nil {
		return out
	}
	t := history.Info
	var pe *postgresError
	var se *sentError

	switch {
	case op.F == history.FRead:
		t = history.Fail // reads never mutate state
	case errors.As(err, &pe):
		// Definite rejection by PostgreSQL
		switch pe.code {
		case "40001", "40P01", "23505", "25P02", "25006": // serialization_failure, deadlock, unique_violation, read_only
			t = history.Fail
		default:
			t = history.Fail
		}
	case !errors.As(err, &se):
		t = history.Fail // connection never established
	}

	out = complete(op, t)
	out.Error = err.Error()
	return out
}

func (p *Postgres) invoke(ctx context.Context, op history.Op) (history.Op, error) {
	if err := p.ensureSchema(ctx); err != nil {
		return history.Op{}, err
	}
	tbl := sanitizeTable(p.prefix + "kv")
	qTbl := sanitizeTable(p.prefix + "queue")

	switch op.F {
	case history.FRead:
		sql := fmt.Sprintf("SELECT v FROM %s WHERE k = '%s';", tbl, escapeSQL(op.Key))
		res, err := p.exec(ctx, sql)
		if err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		if len(res.rows) > 0 && len(res.rows[0]) > 0 && res.rows[0][0] != "" {
			n, err := strconv.ParseInt(res.rows[0][0], 10, 64)
			if err != nil {
				return history.Op{}, fmt.Errorf("postgres: non-integer value: %w", err)
			}
			out.Value = history.Int(n)
		}
		return out, nil

	case history.FWrite:
		val := int64(0)
		if op.Value != nil {
			val = *op.Value
		}
		sql := fmt.Sprintf(`INSERT INTO %s (k, v) VALUES ('%s', %d) ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v;`,
			tbl, escapeSQL(op.Key), val)
		_, err := p.exec(ctx, sql)
		if err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil

	case history.FCas:
		oldVal := int64(0)
		if op.Old != nil {
			oldVal = *op.Old
		}
		newVal := int64(0)
		if op.Value != nil {
			newVal = *op.Value
		}
		sql := fmt.Sprintf(`UPDATE %s SET v = %d WHERE k = '%s' AND v = %d;`,
			tbl, newVal, escapeSQL(op.Key), oldVal)
		res, err := p.exec(ctx, sql)
		if err != nil {
			return history.Op{}, err
		}
		if res.tag == "UPDATE 1" {
			return complete(op, history.OK), nil
		}
		return complete(op, history.Fail), nil

	case history.FTxn:
		// Execute multi-operation transaction
		var b strings.Builder
		b.WriteString("BEGIN;\n")
		for _, m := range op.Txn {
			if m.F == "w" || m.F == "write" {
				val := int64(0)
				if m.Value != nil {
					val = *m.Value
				}
				fmt.Fprintf(&b, "INSERT INTO %s (k, v) VALUES ('%s', %d) ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v;\n",
					tbl, escapeSQL(m.Key), val)
			} else {
				fmt.Fprintf(&b, "SELECT v FROM %s WHERE k = '%s';\n", tbl, escapeSQL(m.Key))
			}
		}
		b.WriteString("COMMIT;")

		res, err := p.exec(ctx, b.String())
		if err != nil {
			_, _ = p.exec(ctx, "ROLLBACK;")
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		out.Txn = make([]history.MicroOp, len(op.Txn))
		copy(out.Txn, op.Txn)

		// Populate observed read values
		rowIdx := 0
		for i, m := range out.Txn {
			if m.F == "r" || m.F == "read" {
				if rowIdx < len(res.rows) && len(res.rows[rowIdx]) > 0 && res.rows[rowIdx][0] != "" {
					n, _ := strconv.ParseInt(res.rows[rowIdx][0], 10, 64)
					out.Txn[i].Value = history.Int(n)
				}
				rowIdx++
			}
		}
		return out, nil

	case history.FEnqueue, "push":
		val := int64(0)
		if op.Value != nil {
			val = *op.Value
		}
		sql := fmt.Sprintf("INSERT INTO %s (q, v) VALUES ('%s', %d);", qTbl, escapeSQL(op.Key), val)
		_, err := p.exec(ctx, sql)
		if err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil

	case history.FDequeue, "pop":
		sql := fmt.Sprintf(`DELETE FROM %s WHERE id = (SELECT id FROM %s WHERE q = '%s' ORDER BY id ASC LIMIT 1) RETURNING v;`,
			qTbl, qTbl, escapeSQL(op.Key))
		res, err := p.exec(ctx, sql)
		if err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		if len(res.rows) > 0 && len(res.rows[0]) > 0 && res.rows[0][0] != "" {
			n, _ := strconv.ParseInt(res.rows[0][0], 10, 64)
			out.Value = history.Int(n)
		}
		return out, nil

	case history.FAdd, history.FInc:
		delta := int64(1)
		if op.Value != nil {
			delta = *op.Value
		}
		sql := fmt.Sprintf(`INSERT INTO %s (k, v) VALUES ('%s', %d) ON CONFLICT (k) DO UPDATE SET v = %s.v + %d;`,
			tbl, escapeSQL(op.Key), delta, tbl, delta)
		_, err := p.exec(ctx, sql)
		if err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil
	}

	return history.Op{}, &postgresError{code: "0A000", msg: "unsupported operation " + op.F}
}

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
