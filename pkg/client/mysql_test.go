package client

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// mockMySQLServer implements a minimal in-memory MySQL v10 wire protocol server.
type mockMySQLServer struct {
	listener     net.Listener
	addr         string
	store        map[string]int64
	mu           sync.Mutex
	failDeadlock bool
}

func startMockMySQL(t *testing.T) *mockMySQLServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock MySQL listener: %v", err)
	}
	s := &mockMySQLServer{
		listener: ln,
		addr:     ln.Addr().String(),
		store:    make(map[string]int64),
	}
	go s.serve()
	return s
}

func (s *mockMySQLServer) close() {
	_ = s.listener.Close()
}

func (s *mockMySQLServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func writePkt(w io.Writer, seq uint8, data []byte) error {
	length := len(data)
	hdr := [4]byte{
		byte(length),
		byte(length >> 8),
		byte(length >> 16),
		seq,
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readPkt(r io.Reader) (uint8, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	length := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	seq := hdr[3]
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return seq, payload, nil
}

func (s *mockMySQLServer) handleConn(conn net.Conn) {
	defer conn.Close()

	// 1. Send Handshake v10 packet
	var hs []byte
	hs = append(hs, 10) // Protocol v10
	hs = append(hs, []byte("5.7.25-TiDB-v7.1.0\x00")...)
	hs = binary.LittleEndian.AppendUint32(hs, 1) // connection id
	salt := []byte("01234567890123456789")
	hs = append(hs, salt[:8]...)
	hs = append(hs, 0)                                 // filler
	hs = binary.LittleEndian.AppendUint16(hs, 0xffff)  // capabilities 1
	hs = append(hs, 45)                                // charset
	hs = binary.LittleEndian.AppendUint16(hs, 0x0002)  // status
	hs = binary.LittleEndian.AppendUint16(hs, 0xffff)  // capabilities 2
	hs = append(hs, 21)                                // auth plugin data len
	hs = append(hs, make([]byte, 10)...)               // reserved
	hs = append(hs, salt[8:20]...)
	hs = append(hs, []byte("mysql_native_password\x00")...)

	if err := writePkt(conn, 0, hs); err != nil {
		return
	}

	// 2. Read Handshake Response 4.1
	seq, _, err := readPkt(conn)
	if err != nil {
		return
	}

	// 3. Send Auth OK Packet
	okPkt := []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
	if err := writePkt(conn, seq+1, okPkt); err != nil {
		return
	}

	// 4. Command loop
	for {
		_, cmdPkt, err := readPkt(conn)
		if err != nil {
			return
		}
		if len(cmdPkt) == 0 {
			continue
		}
		cmd := cmdPkt[0]
		if cmd == 0x01 { // COM_QUIT
			return
		}
		if cmd != 0x03 { // COM_QUERY
			continue
		}

		sql := string(cmdPkt[1:])
		s.handleQuery(conn, sql)
	}
}

func (s *mockMySQLServer) handleQuery(conn net.Conn, sql string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failDeadlock && strings.HasPrefix(sql, "INSERT") {
		// Send Deadlock Error Packet (1213, SQLSTATE 40001)
		var errPkt []byte
		errPkt = append(errPkt, 0xff)
		errPkt = binary.LittleEndian.AppendUint16(errPkt, 1213)
		errPkt = append(errPkt, '#')
		errPkt = append(errPkt, []byte("40001")...)
		errPkt = append(errPkt, []byte("Deadlock found when trying to get lock; try restarting transaction")...)
		_ = writePkt(conn, 1, errPkt)
		return
	}

	// DDL / Transaction control commands
	if strings.HasPrefix(sql, "CREATE TABLE") || strings.HasPrefix(sql, "START TRANSACTION") ||
		strings.HasPrefix(sql, "COMMIT") || strings.HasPrefix(sql, "ROLLBACK") {
		okPkt := []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
		_ = writePkt(conn, 1, okPkt)
		return
	}

	// Read: SELECT v FROM ... WHERE k = 'x'
	if strings.HasPrefix(sql, "SELECT v FROM") {
		key := extractKey(sql)
		val, exists := s.store[key]

		// 1. Column Count Packet
		_ = writePkt(conn, 1, []byte{0x01})

		// 2. Column Def Packet
		colDef := makeColumnDef("v")
		_ = writePkt(conn, 2, colDef)

		// 3. Column EOF Packet
		eofPkt := []byte{0xfe, 0x00, 0x00, 0x02, 0x00}
		_ = writePkt(conn, 3, eofPkt)

		if exists {
			// Row Packet
			valStr := []byte(strings.TrimSpace(string([]byte{byte('0' + val)})))
			if val >= 10 {
				valStr = []byte(strings.TrimSpace(string([]byte{byte('0' + val/10), byte('0' + val%10)})))
			}
			rowPkt := append([]byte{byte(len(valStr))}, valStr...)
			_ = writePkt(conn, 4, rowPkt)
		}

		// 4. Final EOF Packet
		_ = writePkt(conn, 5, eofPkt)
		return
	}

	// Write: INSERT INTO ... ON DUPLICATE KEY UPDATE
	if strings.HasPrefix(sql, "INSERT INTO") {
		key := extractKey(sql)
		val := extractVal(sql)
		s.store[key] = val

		// OK packet with affectedRows = 1
		okPkt := []byte{0x00, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00}
		_ = writePkt(conn, 1, okPkt)
		return
	}

	// CAS: UPDATE ... SET v = new WHERE k = 'x' AND v = old
	if strings.HasPrefix(sql, "UPDATE") {
		key := extractKey(sql)
		newVal := extractVal(sql)
		oldVal := extractOldVal(sql)

		curVal, exists := s.store[key]
		affected := byte(0)
		if exists && curVal == oldVal {
			s.store[key] = newVal
			affected = 1
		}

		okPkt := []byte{0x00, affected, 0x00, 0x02, 0x00, 0x00, 0x00}
		_ = writePkt(conn, 1, okPkt)
		return
	}

	// Default OK
	_ = writePkt(conn, 1, []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00})
}

func makeColumnDef(name string) []byte {
	var b []byte
	b = append(b, []byte("\x03def\x00\x00\x00")...)
	b = append(b, byte(len(name)))
	b = append(b, []byte(name)...)
	b = append(b, byte(len(name)))
	b = append(b, []byte(name)...)
	b = append(b, 0x0c, 0x3f, 0x00)                  // len, charset
	b = binary.LittleEndian.AppendUint32(b, 20)      // column length
	b = append(b, 0x08)                              // type LONGLONG
	b = binary.LittleEndian.AppendUint16(b, 0x0000)  // flags
	b = append(b, 0x00, 0x00, 0x00)                  // decimals, filler
	return b
}

func extractKey(sql string) string {
	parts := strings.Split(sql, "'")
	if len(parts) >= 2 {
		return parts[1]
	}
	return "x"
}

func extractVal(sql string) int64 {
	idx := strings.Index(sql, "VALUES")
	if idx != -1 {
		sub := sql[idx:]
		p := strings.Split(sub, ",")
		if len(p) >= 2 {
			cleaned := strings.Trim(strings.TrimSpace(p[1]), "() ;")
			if len(cleaned) > 0 && cleaned[0] >= '0' && cleaned[0] <= '9' {
				return int64(cleaned[0] - '0')
			}
		}
	}
	idx = strings.Index(sql, "SET v = ")
	if idx != -1 {
		sub := sql[idx+8:]
		p := strings.Split(sub, " ")
		if len(p) > 0 && len(p[0]) > 0 && p[0][0] >= '0' && p[0][0] <= '9' {
			return int64(p[0][0] - '0')
		}
	}
	return 0
}

func extractOldVal(sql string) int64 {
	idx := strings.Index(sql, "AND v = ")
	if idx != -1 {
		sub := sql[idx+8:]
		p := strings.Split(sub, ";")
		cleaned := strings.TrimSpace(p[0])
		if len(cleaned) > 0 && cleaned[0] >= '0' && cleaned[0] <= '9' {
			return int64(cleaned[0] - '0')
		}
	}
	return 0
}

func TestMySQLClientReadWrite(t *testing.T) {
	srv := startMockMySQL(t)
	defer srv.close()

	cli := NewMySQL(srv.addr, "test/")
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Initial Read on empty key should return nil
	r1 := cli.Invoke(ctx, history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FRead,
		Key:     "k0",
	})
	if r1.Type != history.OK || r1.Value != nil {
		t.Fatalf("expected OK with nil value on initial read, got %+v", r1)
	}

	// 2. Write value 3
	w1 := cli.Invoke(ctx, history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FWrite,
		Key:     "k0",
		Value:   history.Int(3),
	})
	if w1.Type != history.OK {
		t.Fatalf("expected write to succeed, got %+v", w1)
	}

	// 3. Read back written value
	r2 := cli.Invoke(ctx, history.Op{
		Process: 1,
		Type:    history.Invoke,
		F:       history.FRead,
		Key:     "k0",
	})
	if r2.Type != history.OK || r2.Value == nil || *r2.Value != 3 {
		t.Fatalf("expected OK with value 3, got %+v", r2)
	}
}

func TestMySQLClientCAS(t *testing.T) {
	srv := startMockMySQL(t)
	defer srv.close()

	cli := NewMySQL(srv.addr, "test_cas/")
	defer cli.Close()

	ctx := context.Background()

	// Initial write 5
	_ = cli.Invoke(ctx, history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FWrite,
		Key:     "k1",
		Value:   history.Int(5),
	})

	// CAS with wrong old value (expected 5, supplied 4) -> Fail
	casFail := cli.Invoke(ctx, history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FCas,
		Key:     "k1",
		Old:     history.Int(4),
		Value:   history.Int(6),
	})
	if casFail.Type != history.Fail {
		t.Fatalf("expected CAS mismatch to fail, got %+v", casFail)
	}

	// CAS with matching old value (5 -> 6) -> OK
	casOk := cli.Invoke(ctx, history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FCas,
		Key:     "k1",
		Old:     history.Int(5),
		Value:   history.Int(6),
	})
	if casOk.Type != history.OK || casOk.Value == nil || *casOk.Value != 6 {
		t.Fatalf("expected CAS success with value 6, got %+v", casOk)
	}
}

func TestMySQLDeadlockErrorClassification(t *testing.T) {
	srv := startMockMySQL(t)
	defer srv.close()

	srv.mu.Lock()
	srv.failDeadlock = true
	srv.mu.Unlock()

	cli := NewMySQL(srv.addr, "test_deadlock/")
	defer cli.Close()

	ctx := context.Background()
	res := cli.Invoke(ctx, history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FWrite,
		Key:     "k2",
		Value:   history.Int(10),
	})

	// Deadlock must be classified as definite Fail (rolled back by database), not Info
	if res.Type != history.Fail {
		t.Fatalf("expected deadlock error to be classified as Fail, got %v", res.Type)
	}
	if !strings.Contains(res.Error, "Deadlock") {
		t.Fatalf("expected deadlock error message, got: %s", res.Error)
	}
}
