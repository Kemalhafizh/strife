package client

import (
	"bufio"
	"bytes"
	"testing"
	"time"
)

// The protocol parsers read bytes from the network; whatever arrives, they
// must return an error rather than panic, loop or allocate without bound.

func FuzzRESP(f *testing.F) {
	for _, s := range []string{"+OK\r\n", "-ERR x\r\n", ":42\r\n", "$3\r\nabc\r\n", "$-1\r\n", "*2\r\n$1\r\na\r\n*1\r\n:1\r\n", "*-1\r\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rd := bufio.NewReader(bytes.NewReader(data))
		for i := 0; i < 8; i++ {
			if _, err := readReply(rd); err != nil {
				return
			}
		}
	})
}

func FuzzPostgresMessages(f *testing.F) {
	f.Add(pgRow("42", "1,2")[5:])
	f.Add([]byte("CXX000\x00Mboom\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseDataRow(data)
		_ = parseErrorResponse(data)
		_, _ = parseList(string(data))
	})
}

func FuzzMySQLPackets(f *testing.F) {
	var seed []byte
	seq := byte(1)
	for _, p := range append(myRows(2, str("1,2"), nil), myOK(2), myErr(1213, "40001", "deadlock")) {
		seed = append(seed, byte(len(p)), byte(len(p)>>8), byte(len(p)>>16), seq)
		seed = append(seed, p...)
		seq++
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _, _ = lenenc(data)
		_ = parseMySQLError(data)
		m := &MySQL{rd: bufio.NewReader(bytes.NewReader(data))}
		for i := 0; i < 4; i++ {
			if _, more, err := m.readResult(); err != nil || !more {
				return
			}
		}
	})
}

func FuzzKafkaRecords(f *testing.F) {
	f.Add(recordBatch(7, time.Unix(1, 0)), int64(0))
	f.Fuzz(func(t *testing.T, data []byte, from int64) {
		_, _, _ = parseRecords(data, from)
		_, _, _, _ = parseMetadata(data, "t")
	})
}
