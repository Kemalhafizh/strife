package client

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// MySQL speaks the MySQL Client/Server Protocol v10 to MySQL, Percona, MariaDB,
// or TiDB using the Go standard library only.
//
// Operations supported:
//   - read:     SELECT v FROM <table> WHERE k = '<key>';
//   - write:    INSERT INTO <table> (k, v) VALUES ('<key>', <v>) ON DUPLICATE KEY UPDATE v = VALUES(v);
//   - cas:      UPDATE <table> SET v = <new> WHERE k = '<key>' AND v = <old>;
//   - inc/add:  INSERT INTO <table> (k, v) VALUES ('<key>', <delta>) ON DUPLICATE KEY UPDATE v = <table>.v + VALUES(v);
//   - enqueue:  INSERT INTO <queue_table> (q, v) VALUES ('<key>', <v>);
//   - dequeue:  atomic SELECT ... FOR UPDATE + DELETE inside mini-transaction;
//   - txn:      atomic multi-operation transaction (START TRANSACTION ... micro-ops ... COMMIT).
//
// Error classification:
//   - MySQL error 1213 (ER_LOCK_DEADLOCK): definite fail (rolled back by database);
//   - MySQL error 1205 (ER_LOCK_WAIT_TIMEOUT): definite fail;
//   - MySQL error 1062 (ER_DUP_ENTRY): definite fail;
//   - TiDB error 9007 (ErrWriteConflict) / 9008 (ErrTxnRetryable): definite fail;
//   - SQLSTATE 40001 (serialization failure): definite fail;
//   - Connection failure before query sent: definite fail;
//   - Timeout or connection drop on write/commit after sending: info (outcome indeterminate);
//   - Reads: always safe to call fail.
type MySQL struct {
	addr     string
	prefix   string
	user     string
	password string
	database string
	conn     net.Conn
	rd       *bufio.Reader
	seq      uint8
	initDone bool
	mu       sync.Mutex
}

// NewMySQL returns a MySQL / TiDB client for one node endpoint (e.g. "127.0.0.1:4000" or "127.0.0.1:3306").
func NewMySQL(addr, prefix string) *MySQL {
	return &MySQL{
		addr:     addr,
		prefix:   prefix,
		user:     "root",
		password: "",
		database: "",
	}
}

// Close implements Client.
func (m *MySQL) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return nil
	}
	// Try sending COM_QUIT (0x01)
	_ = m.writePacketRaw([]byte{0x01})
	err := m.conn.Close()
	m.conn, m.rd = nil, nil
	return err
}

type mysqlError struct {
	code     uint16
	sqlstate string
	msg      string
}

func (e *mysqlError) Error() string {
	return fmt.Sprintf("mysql: [%d] (%s) %s", e.code, e.sqlstate, e.msg)
}

func (m *MySQL) writePacketRaw(data []byte) error {
	length := len(data)
	header := [4]byte{
		byte(length),
		byte(length >> 8),
		byte(length >> 16),
		m.seq,
	}
	m.seq++
	if _, err := m.conn.Write(header[:]); err != nil {
		return err
	}
	_, err := m.conn.Write(data)
	return err
}

func (m *MySQL) readPacketRaw() ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(m.rd, header[:]); err != nil {
		return nil, err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	m.seq = header[3] + 1
	payload := make([]byte, length)
	if _, err := io.ReadFull(m.rd, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func scramblePassword(salt []byte, password string) []byte {
	if len(password) == 0 {
		return nil
	}
	h1 := sha1.Sum([]byte(password))
	h2 := sha1.Sum(h1[:])

	h := sha1.New()
	if len(salt) >= 20 {
		h.Write(salt[:20])
	} else {
		h.Write(salt)
	}
	h.Write(h2[:])
	candidate := h.Sum(nil)

	scramble := make([]byte, 20)
	for i := 0; i < 20 && i < len(candidate); i++ {
		scramble[i] = h1[i] ^ candidate[i]
	}
	return scramble
}

func parseMySQLError(payload []byte) *mysqlError {
	if len(payload) < 3 {
		return &mysqlError{code: 0, msg: "unknown error packet"}
	}
	code := binary.LittleEndian.Uint16(payload[1:3])
	sqlstate := "HY000"
	msg := ""
	if len(payload) >= 9 && payload[3] == '#' {
		sqlstate = string(payload[4:9])
		msg = string(payload[9:])
	} else if len(payload) > 3 {
		msg = string(payload[3:])
	}
	return &mysqlError{code: code, sqlstate: sqlstate, msg: msg}
}

func readLengthEncodedInt(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	switch b[0] {
	case 0xfb:
		return 0, 1 // NULL
	case 0xfc:
		if len(b) < 3 {
			return 0, 0
		}
		return uint64(binary.LittleEndian.Uint16(b[1:3])), 3
	case 0xfd:
		if len(b) < 4 {
			return 0, 0
		}
		return uint64(b[1]) | uint64(b[2])<<8 | uint64(b[3])<<16, 4
	case 0xfe:
		if len(b) < 9 {
			return 0, 0
		}
		return binary.LittleEndian.Uint64(b[1:9]), 9
	default:
		return uint64(b[0]), 1
	}
}

func parseRowValues(b []byte, colCount int) []string {
	vals := make([]string, colCount)
	offset := 0
	for i := 0; i < colCount; i++ {
		if offset >= len(b) {
			break
		}
		if b[offset] == 0xfb {
			vals[i] = "NULL"
			offset++
			continue
		}
		length, n := readLengthEncodedInt(b[offset:])
		offset += n
		if offset+int(length) <= len(b) {
			vals[i] = string(b[offset : offset+int(length)])
			offset += int(length)
		}
	}
	return vals
}

func (m *MySQL) dial(ctx context.Context) error {
	if m.conn != nil {
		return nil
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", m.addr)
	if err != nil {
		return err
	}
	m.conn, m.rd = c, bufio.NewReader(c)
	m.seq = 0

	// 1. Read Initial Handshake Packet
	handshake, err := m.readPacketRaw()
	if err != nil {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return fmt.Errorf("mysql: failed to read handshake: %w", err)
	}
	if len(handshake) > 0 && handshake[0] == 0xff {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return parseMySQLError(handshake)
	}

	// Parse salt from handshake
	salt := make([]byte, 20)
	idx := 1
	for idx < len(handshake) && handshake[idx] != 0 {
		idx++
	}
	idx++    // skip null byte of server version
	idx += 4 // skip connection id
	if idx+8 <= len(handshake) {
		copy(salt[0:8], handshake[idx:idx+8])
	}
	idx += 8 + 1 // 8 bytes salt + 1 filler byte
	idx += 2     // capability flags part 1

	if idx < len(handshake) {
		idx += 1 // charset
		idx += 2 // status flags
		idx += 2 // capability flags part 2
		authLen := 21
		if idx < len(handshake) {
			authLen = int(handshake[idx])
			idx++
		}
		idx += 10 // reserved
		part2Len := authLen - 8
		if part2Len < 12 {
			part2Len = 12
		}
		if idx+part2Len <= len(handshake) {
			copy(salt[8:20], handshake[idx:idx+part2Len])
		}
	}

	// 2. Send Handshake Response 4.1
	clientFlags := uint32(
		0x00000001 | // CLIENT_LONG_PASSWORD
			0x00000004 | // CLIENT_LONG_FLAG
			0x00000200 | // CLIENT_PROTOCOL_41
			0x00008000 | // CLIENT_SECURE_CONNECTION
			0x00080000 | // CLIENT_PLUGIN_AUTH
			0x00010000,  // CLIENT_MULTI_STATEMENTS
	)
	if m.database != "" {
		clientFlags |= 0x00000008 // CLIENT_CONNECT_WITH_DB
	}

	var resp []byte
	resp = binary.LittleEndian.AppendUint32(resp, clientFlags)
	resp = binary.LittleEndian.AppendUint32(resp, 0x01000000) // max packet size 16MB
	resp = append(resp, 45)                                   // utf8mb4 charset (0x2d)
	resp = append(resp, make([]byte, 23)...)                  // 23 filler bytes
	resp = append(resp, []byte(m.user)...)
	resp = append(resp, 0) // null-terminated username

	scramble := scramblePassword(salt, m.password)
	if len(scramble) > 0 {
		resp = append(resp, byte(len(scramble)))
		resp = append(resp, scramble...)
	} else {
		resp = append(resp, 0)
	}

	if m.database != "" {
		resp = append(resp, []byte(m.database)...)
		resp = append(resp, 0)
	}
	resp = append(resp, []byte("mysql_native_password\x00")...)

	if err := m.writePacketRaw(resp); err != nil {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return fmt.Errorf("mysql: failed to write auth response: %w", err)
	}

	// 3. Read Auth Result
	authRes, err := m.readPacketRaw()
	if err != nil {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return fmt.Errorf("mysql: failed to read auth result: %w", err)
	}
	if len(authRes) > 0 && authRes[0] == 0xff {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return parseMySQLError(authRes)
	}

	return nil
}

type mysqlQueryResult struct {
	affectedRows uint64
	rows         [][]string
}

func (m *MySQL) execQuery(ctx context.Context, sql string) (*mysqlQueryResult, error) {
	if err := m.dial(ctx); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = m.conn.SetDeadline(deadline)
		defer func() { _ = m.conn.SetDeadline(time.Time{}) }()
	}

	m.seq = 0
	cmd := append([]byte{0x03}, []byte(sql)...) // 0x03 = COM_QUERY
	if err := m.writePacketRaw(cmd); err != nil {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return nil, err
	}

	firstPkt, err := m.readPacketRaw()
	if err != nil {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return nil, err
	}

	if len(firstPkt) == 0 {
		return &mysqlQueryResult{}, nil
	}

	// Error packet
	if firstPkt[0] == 0xff {
		return nil, parseMySQLError(firstPkt)
	}

	// OK packet
	if firstPkt[0] == 0x00 {
		affected, _ := readLengthEncodedInt(firstPkt[1:])
		return &mysqlQueryResult{affectedRows: affected}, nil
	}

	// Result Set
	colCount, _ := readLengthEncodedInt(firstPkt)
	for i := 0; i < int(colCount); i++ {
		if _, err := m.readPacketRaw(); err != nil {
			_ = m.conn.Close()
			m.conn, m.rd = nil, nil
			return nil, err
		}
	}

	// Read column EOF / OK packet
	colEOF, err := m.readPacketRaw()
	if err != nil {
		_ = m.conn.Close()
		m.conn, m.rd = nil, nil
		return nil, err
	}
	if len(colEOF) > 0 && colEOF[0] == 0xff {
		return nil, parseMySQLError(colEOF)
	}

	// Read Rows until EOF (0xfe with length < 9) or OK packet
	var rows [][]string
	for {
		rowPkt, err := m.readPacketRaw()
		if err != nil {
			_ = m.conn.Close()
			m.conn, m.rd = nil, nil
			return nil, err
		}
		if len(rowPkt) > 0 {
			if rowPkt[0] == 0xff {
				return nil, parseMySQLError(rowPkt)
			}
			if rowPkt[0] == 0xfe && len(rowPkt) < 9 {
				break // EOF packet
			}
			if rowPkt[0] == 0x00 && len(rowPkt) < 9 {
				break
			}
		}
		rows = append(rows, parseRowValues(rowPkt, int(colCount)))
	}

	return &mysqlQueryResult{rows: rows}, nil
}

func mysqlTableName(prefix string) string {
	s := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, prefix)
	s = strings.Trim(s, "_")
	if s == "" {
		s = "strife"
	}
	return s + "_kv"
}

func mysqlQueueTableName(prefix string) string {
	return mysqlTableName(prefix) + "_queue"
}

func (m *MySQL) ensureSchema(ctx context.Context) error {
	if m.initDone {
		return nil
	}
	table := mysqlTableName(m.prefix)
	qTable := mysqlQueueTableName(m.prefix)

	schema := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		k VARCHAR(255) PRIMARY KEY,
		v BIGINT NOT NULL
	) ENGINE=InnoDB;`, table)
	if _, err := m.execQuery(ctx, schema); err != nil {
		return err
	}

	qSchema := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		q VARCHAR(255) NOT NULL,
		v BIGINT NOT NULL,
		INDEX idx_q_id (q, id)
	) ENGINE=InnoDB;`, qTable)
	if _, err := m.execQuery(ctx, qSchema); err != nil {
		return err
	}

	m.initDone = true
	return nil
}

func classifyMySQLError(op history.Op, err error) history.Type {
	if op.F == history.FRead {
		return history.Fail
	}
	var myErr *mysqlError
	if errors.As(err, &myErr) {
		switch myErr.code {
		case 1213, 1205, 1062, 3572, 9007, 9008, 8005, 1045, 1146:
			// Definite server-side aborts / lock wait timeouts
			return history.Fail
		}
		if myErr.sqlstate == "40001" || myErr.sqlstate == "23000" {
			return history.Fail
		}
	}
	return Classify(op, err)
}

// Invoke executes one operation against MySQL or TiDB.
func (m *MySQL) Invoke(ctx context.Context, op history.Op) history.Op {
	m.mu.Lock()
	defer m.mu.Unlock()

	res := op
	res.Time = time.Now().UnixNano()

	if err := m.ensureSchema(ctx); err != nil {
		res.Type = classifyMySQLError(op, err)
		res.Error = err.Error()
		return res
	}

	table := mysqlTableName(m.prefix)
	qTable := mysqlQueueTableName(m.prefix)

	switch op.F {
	case history.FRead:
		sql := fmt.Sprintf("SELECT v FROM %s WHERE k = '%s';", table, op.Key)
		qr, err := m.execQuery(ctx, sql)
		if err != nil {
			res.Type = history.Fail
			res.Error = err.Error()
			return res
		}
		res.Type = history.OK
		if len(qr.rows) > 0 && len(qr.rows[0]) > 0 && qr.rows[0][0] != "NULL" {
			if v, err := strconv.ParseInt(qr.rows[0][0], 10, 64); err == nil {
				res.Value = history.Int(v)
			}
		} else {
			res.Value = nil
		}
		return res

	case history.FWrite:
		val := int64(0)
		if op.Value != nil {
			val = *op.Value
		}
		sql := fmt.Sprintf("INSERT INTO %s (k, v) VALUES ('%s', %d) ON DUPLICATE KEY UPDATE v = VALUES(v);", table, op.Key, val)
		_, err := m.execQuery(ctx, sql)
		if err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		res.Type = history.OK
		return res

	case history.FCas:
		oldVal, newVal := int64(0), int64(0)
		if op.Old != nil {
			oldVal = *op.Old
		}
		if op.Value != nil {
			newVal = *op.Value
		}
		sql := fmt.Sprintf("UPDATE %s SET v = %d WHERE k = '%s' AND v = %d;", table, newVal, op.Key, oldVal)
		qr, err := m.execQuery(ctx, sql)
		if err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		if qr.affectedRows > 0 {
			res.Type = history.OK
			res.Value = history.Int(newVal)
		} else {
			res.Type = history.Fail
			res.Error = "cas condition mismatch"
		}
		return res

	case history.FAdd, history.FInc:
		delta := int64(1)
		if op.Value != nil {
			delta = *op.Value
		}
		sql := fmt.Sprintf("INSERT INTO %s (k, v) VALUES ('%s', %d) ON DUPLICATE KEY UPDATE v = %s.v + VALUES(v);", table, op.Key, delta, table)
		if _, err := m.execQuery(ctx, sql); err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		readSQL := fmt.Sprintf("SELECT v FROM %s WHERE k = '%s';", table, op.Key)
		qr, err := m.execQuery(ctx, readSQL)
		if err != nil {
			res.Type = history.Info
			res.Error = err.Error()
			return res
		}
		res.Type = history.OK
		if len(qr.rows) > 0 && len(qr.rows[0]) > 0 {
			if v, err := strconv.ParseInt(qr.rows[0][0], 10, 64); err == nil {
				res.Value = history.Int(v)
			}
		}
		return res

	case history.FEnqueue:
		val := int64(0)
		if op.Value != nil {
			val = *op.Value
		}
		sql := fmt.Sprintf("INSERT INTO %s (q, v) VALUES ('%s', %d);", qTable, op.Key, val)
		if _, err := m.execQuery(ctx, sql); err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		res.Type = history.OK
		return res

	case history.FDequeue:
		if _, err := m.execQuery(ctx, "START TRANSACTION;"); err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		selectSQL := fmt.Sprintf("SELECT id, v FROM %s WHERE q = '%s' ORDER BY id ASC LIMIT 1 FOR UPDATE;", qTable, op.Key)
		qr, err := m.execQuery(ctx, selectSQL)
		if err != nil {
			_, _ = m.execQuery(ctx, "ROLLBACK;")
			res.Type = history.Fail
			res.Error = err.Error()
			return res
		}
		if len(qr.rows) == 0 {
			_, _ = m.execQuery(ctx, "COMMIT;")
			res.Type = history.OK
			res.Value = nil
			return res
		}
		rowID := qr.rows[0][0]
		val, _ := strconv.ParseInt(qr.rows[0][1], 10, 64)
		delSQL := fmt.Sprintf("DELETE FROM %s WHERE id = %s;", qTable, rowID)
		if _, err := m.execQuery(ctx, delSQL); err != nil {
			_, _ = m.execQuery(ctx, "ROLLBACK;")
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		if _, err := m.execQuery(ctx, "COMMIT;"); err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		res.Type = history.OK
		res.Value = history.Int(val)
		return res

	case history.FTxn:
		if _, err := m.execQuery(ctx, "START TRANSACTION;"); err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		var completedTxn []history.MicroOp
		for _, mop := range op.Txn {
			mres := mop
			switch mop.F {
			case "r", history.FRead:
				sql := fmt.Sprintf("SELECT v FROM %s WHERE k = '%s';", table, mop.Key)
				qr, err := m.execQuery(ctx, sql)
				if err != nil {
					_, _ = m.execQuery(ctx, "ROLLBACK;")
					res.Type = classifyMySQLError(op, err)
					res.Error = err.Error()
					return res
				}
				if len(qr.rows) > 0 && len(qr.rows[0]) > 0 && qr.rows[0][0] != "NULL" {
					if v, err := strconv.ParseInt(qr.rows[0][0], 10, 64); err == nil {
						mres.Value = history.Int(v)
					}
				} else {
					mres.Value = nil
				}
			case "w", history.FWrite:
				val := int64(0)
				if mop.Value != nil {
					val = *mop.Value
				}
				sql := fmt.Sprintf("INSERT INTO %s (k, v) VALUES ('%s', %d) ON DUPLICATE KEY UPDATE v = VALUES(v);", table, mop.Key, val)
				if _, err := m.execQuery(ctx, sql); err != nil {
					_, _ = m.execQuery(ctx, "ROLLBACK;")
					res.Type = classifyMySQLError(op, err)
					res.Error = err.Error()
					return res
				}
			}
			completedTxn = append(completedTxn, mres)
		}
		if _, err := m.execQuery(ctx, "COMMIT;"); err != nil {
			res.Type = classifyMySQLError(op, err)
			res.Error = err.Error()
			return res
		}
		res.Type = history.OK
		res.Txn = completedTxn
		return res
	}

	res.Type = history.Fail
	res.Error = fmt.Sprintf("mysql: unsupported function %q", op.F)
	return res
}
