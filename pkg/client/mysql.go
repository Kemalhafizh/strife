package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // required by mysql_native_password and RSA-OAEP
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// MySQL speaks the MySQL client/server protocol (v10 handshake, protocol
// 4.1) to MySQL, MariaDB and TiDB with the standard library only. It
// authenticates with caching_sha2_password (the MySQL 8 default, including
// full authentication over TLS or with the server's RSA key) and
// mysql_native_password, follows authentication switch requests, and can
// encrypt the connection with TLS.
//
// Operations are those of the PostgreSQL client: read / write / cas / add on
// single keys and list-append transactions, which run as one multi-statement
// query "SET TRANSACTION ISOLATION LEVEL ...; START TRANSACTION; ...;
// COMMIT", so that the server stops at the first error and COMMIT only runs
// when everything before it succeeded.
//
// Classification of a failed operation:
//   - read-only operations: always fail;
//   - a statement before COMMIT failed: fail (the transaction is rolled back);
//   - errors that roll back for sure (deadlock 1213, lock wait 1205,
//     duplicate key 1062, read-only 1290/1792, TiDB write conflict 9007,
//     SQLSTATE 40001, syntax/data errors): fail;
//   - lost connections, timeouts, shutdowns and unknown errors on a write or
//     a COMMIT: info.
type MySQL struct {
	addr, prefix string
	opt          ConnOptions
	conn         net.Conn
	rd           *bufio.Reader
	seq          uint8
	schema       bool
}

// NewMySQL returns a client for one MySQL or TiDB node ("10.0.0.1:3306").
func NewMySQL(addr, prefix string, opt ConnOptions) *MySQL {
	if opt.User == "" {
		opt.User = "root"
	}
	if opt.Isolation == "" {
		opt.Isolation = "SERIALIZABLE"
	}
	return &MySQL{addr: addr, prefix: prefix, opt: opt}
}

// Capability flags.
const (
	myLongPassword     = 0x00000001
	myFoundRows        = 0x00000002
	myLongFlag         = 0x00000004
	myConnectWithDB    = 0x00000008
	myProtocol41       = 0x00000200
	mySSL              = 0x00000800
	myTransactions     = 0x00002000
	mySecureConnection = 0x00008000
	myMultiStatements  = 0x00010000
	myMultiResults     = 0x00020000
	myPluginAuth       = 0x00080000
	moreResults        = 0x0008 // SERVER_MORE_RESULTS_EXISTS status flag
)

// Close implements Client.
func (m *MySQL) Close() error {
	if m.conn == nil {
		return nil
	}
	m.seq = 0
	_ = m.writePacket([]byte{0x01}) // COM_QUIT
	err := m.conn.Close()
	m.conn, m.rd = nil, nil
	return err
}

func (m *MySQL) drop() {
	if m.conn != nil {
		_ = m.conn.Close()
	}
	m.conn, m.rd = nil, nil
}

// mysqlError is an ERR packet.
type mysqlError struct {
	code     uint16
	sqlstate string
	msg      string
}

func (e *mysqlError) Error() string {
	return fmt.Sprintf("mysql: [%d] (%s) %s", e.code, e.sqlstate, e.msg)
}

// ambiguous reports whether the error leaves a write's outcome unknown.
func (e *mysqlError) ambiguous() bool {
	switch e.code {
	case 1062, // duplicate entry
		1213, // deadlock: transaction rolled back
		1205, // lock wait timeout (Strife rolls back)
		1290, // --read-only
		1792, // read-only transaction
		1146, // no such table
		1064, // syntax
		1054, // unknown column
		3572, // NOWAIT lock
		1045, // access denied
		8005, // TiDB: write conflict, retry
		9007: // TiDB: write conflict
		return false
	}
	switch {
	case e.sqlstate == "40001",
		strings.HasPrefix(e.sqlstate, "42"),
		strings.HasPrefix(e.sqlstate, "22"),
		strings.HasPrefix(e.sqlstate, "23"):
		return false
	}
	return true
}

func (m *MySQL) writePacket(data []byte) error {
	hdr := []byte{byte(len(data)), byte(len(data) >> 8), byte(len(data) >> 16), m.seq}
	m.seq++
	_, err := m.conn.Write(append(hdr, data...))
	return err
}

func (m *MySQL) readPacket() ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(m.rd, hdr[:]); err != nil {
		return nil, err
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	m.seq = hdr[3] + 1
	p := make([]byte, n)
	_, err := io.ReadFull(m.rd, p)
	return p, err
}

func parseMySQLError(p []byte) *mysqlError {
	e := &mysqlError{sqlstate: "HY000", msg: "malformed error packet"}
	if len(p) < 3 {
		return e
	}
	e.code = binary.LittleEndian.Uint16(p[1:3])
	rest := p[3:]
	if len(rest) >= 6 && rest[0] == '#' {
		e.sqlstate, rest = string(rest[1:6]), rest[6:]
	}
	e.msg = string(rest)
	return e
}

// lenenc reads a length-encoded integer; null is true for 0xfb.
func lenenc(b []byte) (v uint64, n int, null bool, err error) {
	if len(b) == 0 {
		return 0, 0, false, io.ErrUnexpectedEOF
	}
	need := map[byte]int{0xfc: 3, 0xfd: 4, 0xfe: 9}[b[0]]
	switch {
	case b[0] == 0xfb:
		return 0, 1, true, nil
	case need == 0:
		return uint64(b[0]), 1, false, nil
	case len(b) < need:
		return 0, 0, false, io.ErrUnexpectedEOF
	}
	var buf [8]byte
	copy(buf[:], b[1:need])
	return binary.LittleEndian.Uint64(buf[:]), need, false, nil
}

// Authentication scrambles.

func nativePassword(password string, nonce []byte) []byte {
	if password == "" {
		return nil
	}
	h1 := sha1.Sum([]byte(password)) //nolint:gosec
	h2 := sha1.Sum(h1[:])            //nolint:gosec
	h := sha1.New()                  //nolint:gosec
	h.Write(nonce[:min(20, len(nonce))])
	h.Write(h2[:])
	out := h.Sum(nil)
	for i := range out {
		out[i] ^= h1[i]
	}
	return out
}

// cachingSHA2Password is XOR(SHA256(p), SHA256(SHA256(SHA256(p)), nonce)).
func cachingSHA2Password(password string, nonce []byte) []byte {
	if password == "" {
		return nil
	}
	h1 := sha256.Sum256([]byte(password))
	h2 := sha256.Sum256(h1[:])
	h := sha256.New()
	h.Write(h2[:])
	h.Write(nonce[:min(20, len(nonce))])
	out := h.Sum(nil)
	for i := range out {
		out[i] ^= h1[i]
	}
	return out
}

func (m *MySQL) scramble(plugin string, nonce []byte) ([]byte, error) {
	switch plugin {
	case "mysql_native_password":
		return nativePassword(m.opt.Password, nonce), nil
	case "caching_sha2_password":
		return cachingSHA2Password(m.opt.Password, nonce), nil
	case "mysql_clear_password":
		if !m.tlsActive() {
			return nil, fmt.Errorf("mysql: refusing to send a cleartext password without TLS")
		}
		return append([]byte(m.opt.Password), 0), nil
	}
	return nil, fmt.Errorf("mysql: unsupported authentication plugin %q", plugin)
}

func (m *MySQL) tlsActive() bool {
	_, ok := m.conn.(*tls.Conn)
	return ok
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
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	m.conn, m.rd, m.seq = c, bufio.NewReader(c), 0
	if err := m.handshake(ctx); err != nil {
		m.drop()
		return err
	}
	if m.opt.InitSQL != "" {
		if _, err := m.query(ctx, m.opt.InitSQL); err != nil {
			m.drop()
			return fmt.Errorf("mysql: init_sql: %w", err)
		}
	}
	return nil
}

func (m *MySQL) handshake(ctx context.Context) error {
	hs, err := m.readPacket()
	if err != nil {
		return fmt.Errorf("mysql: reading handshake: %w", err)
	}
	if len(hs) > 0 && hs[0] == 0xff {
		return parseMySQLError(hs)
	}
	if len(hs) < 1 || hs[0] != 10 {
		return fmt.Errorf("mysql: unsupported handshake protocol %v", hs[:min(1, len(hs))])
	}
	// protocol(1) version(NUL) connid(4) auth1(8) filler(1) caps-lo(2)
	// charset(1) status(2) caps-hi(2) authlen(1) reserved(10) auth2 plugin(NUL)
	i := bytes.IndexByte(hs[1:], 0) + 2
	if i < 2 || i+4+8+1+2 > len(hs) {
		return fmt.Errorf("mysql: truncated handshake")
	}
	i += 4
	nonce := append([]byte(nil), hs[i:i+8]...)
	i += 9
	caps := uint32(binary.LittleEndian.Uint16(hs[i:]))
	i += 2
	plugin := "mysql_native_password"
	if len(hs) >= i+16 {
		caps |= uint32(binary.LittleEndian.Uint16(hs[i+3:])) << 16
		authLen := int(hs[i+5])
		i += 16
		n := max(13, authLen-8)
		if i+n <= len(hs) {
			nonce = append(nonce, bytes.TrimRight(hs[i:i+n], "\x00")...)
			i += n
		}
		if caps&myPluginAuth != 0 && i < len(hs) {
			if j := bytes.IndexByte(hs[i:], 0); j >= 0 {
				plugin = string(hs[i : i+j])
			} else {
				plugin = string(hs[i:])
			}
		}
	}

	flags := uint32(myLongPassword | myFoundRows | myLongFlag | myProtocol41 | myTransactions |
		mySecureConnection | myMultiStatements | myMultiResults | myPluginAuth)
	if m.opt.Database != "" {
		flags |= myConnectWithDB
	}
	tcfg, err := m.opt.tlsConfig(m.addr)
	if err != nil {
		return err
	}
	head := binary.LittleEndian.AppendUint32(nil, flags)
	head = binary.LittleEndian.AppendUint32(head, 1<<24) // max packet
	head = append(head, 45)                              // utf8mb4
	head = append(head, make([]byte, 23)...)
	if tcfg != nil {
		if caps&mySSL == 0 {
			return fmt.Errorf("mysql: server %s does not support TLS (db.tls=%s)", m.addr, m.opt.TLS)
		}
		binary.LittleEndian.PutUint32(head, flags|mySSL)
		if err := m.writePacket(head); err != nil {
			return err
		}
		tc := tls.Client(m.conn, tcfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("mysql: TLS: %w", err)
		}
		m.conn, m.rd = tc, bufio.NewReader(tc)
		flags |= mySSL
		binary.LittleEndian.PutUint32(head, flags)
	}
	auth, err := m.scramble(plugin, nonce)
	if err != nil {
		return err
	}
	resp := append(append([]byte(nil), head...), m.opt.User...)
	resp = append(resp, 0, byte(len(auth)))
	resp = append(resp, auth...)
	if m.opt.Database != "" {
		resp = append(append(resp, m.opt.Database...), 0)
	}
	resp = append(append(resp, plugin...), 0)
	if err := m.writePacket(resp); err != nil {
		return err
	}
	return m.authResult(plugin, nonce)
}

// authResult follows the server through switch requests and the
// caching_sha2_password exchange until OK or an error.
func (m *MySQL) authResult(plugin string, nonce []byte) error {
	for {
		p, err := m.readPacket()
		if err != nil {
			return fmt.Errorf("mysql: reading auth result: %w", err)
		}
		if len(p) == 0 {
			return fmt.Errorf("mysql: empty auth packet")
		}
		switch p[0] {
		case 0x00:
			return nil
		case 0xff:
			return parseMySQLError(p)
		case 0xfe: // AuthSwitchRequest: plugin NUL nonce
			rest := p[1:]
			j := bytes.IndexByte(rest, 0)
			if j < 0 {
				return fmt.Errorf("mysql: malformed auth switch")
			}
			plugin, nonce = string(rest[:j]), bytes.TrimRight(rest[j+1:], "\x00")
			auth, err := m.scramble(plugin, nonce)
			if err != nil {
				return err
			}
			if err := m.writePacket(auth); err != nil {
				return err
			}
		case 0x01: // AuthMoreData
			if plugin != "caching_sha2_password" || len(p) < 2 {
				return fmt.Errorf("mysql: unexpected auth data for %s", plugin)
			}
			switch p[1] {
			case 3: // fast authentication succeeded; OK follows
			case 4: // full authentication
				if m.tlsActive() {
					if err := m.writePacket(append([]byte(m.opt.Password), 0)); err != nil {
						return err
					}
					continue
				}
				if err := m.writePacket([]byte{2}); err != nil { // request public key
					return err
				}
				key, err := m.readPacket()
				if err != nil {
					return err
				}
				if len(key) < 2 || key[0] != 0x01 {
					return fmt.Errorf("mysql: expected the server's public key")
				}
				enc, err := encryptPassword(m.opt.Password, nonce, key[1:])
				if err != nil {
					return err
				}
				if err := m.writePacket(enc); err != nil {
					return err
				}
			default:
				// Public key sent as AuthMoreData without being asked.
				return fmt.Errorf("mysql: unexpected caching_sha2 state %d", p[1])
			}
		default:
			return fmt.Errorf("mysql: unexpected auth packet 0x%02x", p[0])
		}
	}
}

// encryptPassword encrypts (password NUL) XOR nonce with the server's RSA
// key (RSA-OAEP with SHA-1), for full caching_sha2 authentication without
// TLS.
func encryptPassword(password string, nonce, pemKey []byte) ([]byte, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, fmt.Errorf("mysql: server public key is not PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mysql: server public key: %w", err)
	}
	rk, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("mysql: server public key is not RSA")
	}
	plain := append([]byte(password), 0)
	for i := range plain {
		plain[i] ^= nonce[i%len(nonce)]
	}
	return rsa.EncryptOAEP(sha1.New(), rand.Reader, rk, plain, nil) //nolint:gosec
}

// result is one statement's outcome: rows for a result set, affected rows
// otherwise.
type result struct {
	affected uint64
	rows     [][]*string
}

// query sends COM_QUERY and reads every result. A failed statement ends the
// query (MySQL does not run the statements after it); the results before it
// are returned with the error.
func (m *MySQL) query(ctx context.Context, sql string) ([]result, error) {
	if err := m.dial(ctx); err != nil {
		return nil, &notSent{err}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = m.conn.SetDeadline(dl)
	} else {
		_ = m.conn.SetDeadline(time.Time{})
	}
	m.seq = 0
	if err := m.writePacket(append([]byte{0x03}, sql...)); err != nil {
		m.drop()
		return nil, &sentError{err}
	}
	var out []result
	for {
		r, more, err := m.readResult()
		if err != nil {
			if _, ok := err.(*mysqlError); !ok {
				m.drop()
				return out, &sentError{err}
			}
			return out, err
		}
		out = append(out, r)
		if !more {
			return out, nil
		}
	}
}

func (m *MySQL) readResult() (result, bool, error) {
	p, err := m.readPacket()
	if err != nil {
		return result{}, false, err
	}
	if len(p) == 0 {
		return result{}, false, io.ErrUnexpectedEOF
	}
	switch p[0] {
	case 0xff:
		return result{}, false, parseMySQLError(p)
	case 0x00:
		affected, n, _, err := lenenc(p[1:])
		if err != nil {
			return result{}, false, err
		}
		_, n2, _, err := lenenc(p[1+n:]) // last insert id
		if err != nil || 1+n+n2+2 > len(p) {
			return result{}, false, io.ErrUnexpectedEOF
		}
		status := binary.LittleEndian.Uint16(p[1+n+n2:])
		return result{affected: affected}, status&moreResults != 0, nil
	}
	cols, _, _, err := lenenc(p)
	if err != nil {
		return result{}, false, err
	}
	for i := uint64(0); i < cols; i++ { // column definitions
		if _, err := m.readPacket(); err != nil {
			return result{}, false, err
		}
	}
	if _, err := m.readPacket(); err != nil { // EOF after definitions
		return result{}, false, err
	}
	var r result
	for {
		row, err := m.readPacket()
		if err != nil {
			return result{}, false, err
		}
		if len(row) > 0 && row[0] == 0xff {
			return result{}, false, parseMySQLError(row)
		}
		if len(row) >= 1 && len(row) < 9 && row[0] == 0xfe { // EOF
			more := len(row) >= 5 && binary.LittleEndian.Uint16(row[3:])&moreResults != 0
			return r, more, nil
		}
		vals := make([]*string, 0, cols)
		for off := 0; uint64(len(vals)) < cols; {
			l, n, null, err := lenenc(row[off:])
			if err != nil {
				return result{}, false, err
			}
			off += n
			if null {
				vals = append(vals, nil)
				continue
			}
			if off+int(l) > len(row) {
				return result{}, false, io.ErrUnexpectedEOF
			}
			v := string(row[off : off+int(l)])
			off += int(l)
			vals = append(vals, &v)
		}
		r.rows = append(r.rows, vals)
	}
}

func (m *MySQL) tables() (kv, lists string) {
	base := strings.Trim(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(m.prefix)), "_")
	if base == "" {
		base = "strife"
	}
	return base + "_kv", base + "_lists"
}

func (m *MySQL) ensureSchema(ctx context.Context) error {
	if m.schema {
		return nil
	}
	kv, lists := m.tables()
	sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (k VARCHAR(255) PRIMARY KEY, v BIGINT NOT NULL); CREATE TABLE IF NOT EXISTS %s (k VARCHAR(255) PRIMARY KEY, v LONGTEXT NOT NULL)", kv, lists)
	if _, err := m.query(ctx, sql); err != nil {
		return err
	}
	m.schema = true
	return nil
}

func escapeMySQL(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

// Invoke implements Client.
func (m *MySQL) Invoke(ctx context.Context, op history.Op) history.Op {
	if err := m.ensureSchema(ctx); err != nil {
		out := complete(op, history.Fail) // the operation never ran
		out.Error = err.Error()
		return out
	}
	out, err := m.invoke(ctx, op)
	if err == nil {
		return out
	}
	out = complete(op, sqlOutcome(op, err))
	out.Error = err.Error()
	return out
}

func (m *MySQL) invoke(ctx context.Context, op history.Op) (history.Op, error) {
	kv, lists := m.tables()
	key := escapeMySQL(op.Key)
	switch op.F {
	case history.FRead:
		res, err := m.query(ctx, fmt.Sprintf("SELECT v FROM %s WHERE k = '%s'", kv, key))
		if err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		if len(res) == 1 && len(res[0].rows) == 1 && res[0].rows[0][0] != nil {
			n, err := strconv.ParseInt(*res[0].rows[0][0], 10, 64)
			if err != nil {
				return history.Op{}, &notSent{fmt.Errorf("mysql: non-integer value %q", *res[0].rows[0][0])}
			}
			out.Value = history.Int(n)
		}
		return out, nil
	case history.FWrite:
		if _, err := m.query(ctx, fmt.Sprintf("INSERT INTO %s (k, v) VALUES ('%s', %d) ON DUPLICATE KEY UPDATE v = VALUES(v)", kv, key, *op.Value)); err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil
	case history.FCas:
		// CLIENT_FOUND_ROWS: affected counts matched rows, so a cas to the
		// same value still reports 1.
		res, err := m.query(ctx, fmt.Sprintf("UPDATE %s SET v = %d WHERE k = '%s' AND v = %d", kv, *op.Value, key, *op.Old))
		if err != nil {
			return history.Op{}, err
		}
		if len(res) == 1 && res[0].affected == 1 {
			return complete(op, history.OK), nil
		}
		return complete(op, history.Fail), nil
	case history.FAdd, history.FInc:
		delta := int64(1)
		if op.Value != nil {
			delta = *op.Value
		}
		if _, err := m.query(ctx, fmt.Sprintf("INSERT INTO %[1]s (k, v) VALUES ('%[2]s', %[3]d) ON DUPLICATE KEY UPDATE v = %[1]s.v + VALUES(v)", kv, key, delta)); err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil
	case history.FTxn:
		return m.txn(ctx, op, lists)
	}
	return history.Op{}, &notSent{fmt.Errorf("mysql: unsupported operation %q", op.F)}
}

func (m *MySQL) txn(ctx context.Context, op history.Op, lists string) (history.Op, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "SET TRANSACTION ISOLATION LEVEL %s; START TRANSACTION", m.opt.Isolation)
	for _, mo := range op.Txn {
		k := escapeMySQL(mo.Key)
		switch mo.F {
		case history.MicroAppend:
			fmt.Fprintf(&b, "; INSERT INTO %s (k, v) VALUES ('%s', '%d') ON DUPLICATE KEY UPDATE v = CONCAT(v, ',', VALUES(v))", lists, k, *mo.Value)
		case history.MicroRead:
			fmt.Fprintf(&b, "; SELECT v FROM %s WHERE k = '%s'", lists, k)
		default:
			return history.Op{}, &notSent{fmt.Errorf("mysql: unsupported micro-op %q", mo.F)}
		}
	}
	b.WriteString("; COMMIT")
	commit := len(op.Txn) + 2 // index of COMMIT's result
	res, err := m.query(ctx, b.String())
	if err != nil {
		if m.conn != nil {
			if _, rerr := m.query(ctx, "ROLLBACK"); rerr != nil {
				m.drop()
			}
		}
		var me *mysqlError
		if len(res) < commit && asMySQLError(err, &me) {
			// The server stopped before COMMIT: nothing was committed.
			return history.Op{}, &mysqlError{code: 1213, sqlstate: "40001", msg: "before commit: " + me.Error()}
		}
		return history.Op{}, err
	}
	if len(res) != commit+1 {
		return history.Op{}, &sentError{fmt.Errorf("mysql: %d results for %d statements", len(res), commit+1)}
	}
	out := complete(op, history.OK)
	for i, mo := range op.Txn {
		if mo.F != history.MicroRead {
			continue
		}
		vals := []int64{}
		if r := res[i+2]; len(r.rows) == 1 && r.rows[0][0] != nil {
			var err error
			if vals, err = parseList(*r.rows[0][0]); err != nil {
				return history.Op{}, &sentError{err}
			}
		}
		out.Txn[i].Values = vals
	}
	return out, nil
}

func asMySQLError(err error, target **mysqlError) bool {
	e, ok := err.(*mysqlError)
	if ok {
		*target = e
	}
	return ok
}
