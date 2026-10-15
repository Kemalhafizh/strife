package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Kafka speaks the Kafka Binary Wire Protocol (API Key 0 Produce, API Key 1 Fetch)
// directly to Kafka or Redpanda using only the Go standard library.
//
// Operations supported:
//   - enqueue / write: produces a message to the given topic (partition 0);
//   - dequeue / read: fetches the next message from the given topic at the client's offset.
//
// Error classification:
//   - ErrorCode 5 (LEADER_NOT_AVAILABLE): definite fail (rejected by cluster);
//   - ErrorCode 6 (NOT_LEADER_OR_FOLLOWER): definite fail;
//   - ErrorCode 3 (UNKNOWN_TOPIC_OR_PARTITION): definite fail;
//   - ErrorCode 7 (REQUEST_TIMED_OUT) on produce: info (message may have reached broker);
//   - Dial / connection failure before produce sent: definite fail;
//   - Connection reset or timeout on produce after sending: info;
//   - Reads / fetches: always safe to call fail.
type Kafka struct {
	addr    string
	prefix  string
	conn    net.Conn
	rd      *bufio.Reader
	seq     int32
	mu      sync.Mutex
	offsets map[string]int64 // topic -> consumer offset
}

// NewKafka returns a Kafka client for one broker address (e.g. "127.0.0.1:9092").
func NewKafka(addr, prefix string) *Kafka {
	return &Kafka{
		addr:    addr,
		prefix:  prefix,
		offsets: make(map[string]int64),
	}
}

// Close implements Client.
func (k *Kafka) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn == nil {
		return nil
	}
	err := k.conn.Close()
	k.conn, k.rd = nil, nil
	return err
}

type kafkaError struct {
	code int16
	msg  string
}

func (e *kafkaError) Error() string {
	return fmt.Sprintf("kafka: error code %d (%s)", e.code, e.msg)
}

func kafkaErrorCodeName(code int16) string {
	switch code {
	case 0:
		return "NONE"
	case 1:
		return "OFFSET_OUT_OF_RANGE"
	case 2:
		return "CORRUPT_MESSAGE"
	case 3:
		return "UNKNOWN_TOPIC_OR_PARTITION"
	case 5:
		return "LEADER_NOT_AVAILABLE"
	case 6:
		return "NOT_LEADER_OR_FOLLOWER"
	case 7:
		return "REQUEST_TIMED_OUT"
	default:
		return fmt.Sprintf("ERR_%d", code)
	}
}

func (k *Kafka) dial(ctx context.Context) error {
	if k.conn != nil {
		return nil
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", k.addr)
	if err != nil {
		return err
	}
	k.conn, k.rd = c, bufio.NewReader(c)
	return nil
}

// sendRequest builds a standard Kafka request header and payload, writes it,
// and returns the raw response body.
func (k *Kafka) roundTrip(ctx context.Context, apiKey, apiVersion int16, body []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.dial(ctx); err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = k.conn.SetDeadline(dl)
	} else {
		_ = k.conn.SetDeadline(time.Time{})
	}

	k.seq++
	corrID := k.seq

	// Build request: length(int32), apiKey(int16), apiVersion(int16), correlationId(int32), clientId("strife")
	var header bytes.Buffer
	_ = binary.Write(&header, binary.BigEndian, apiKey)
	_ = binary.Write(&header, binary.BigEndian, apiVersion)
	_ = binary.Write(&header, binary.BigEndian, corrID)

	clientID := "strife"
	_ = binary.Write(&header, binary.BigEndian, int16(len(clientID)))
	header.WriteString(clientID)

	reqLen := int32(header.Len() + len(body))
	if err := binary.Write(k.conn, binary.BigEndian, reqLen); err != nil {
		_ = k.conn.Close()
		k.conn, k.rd = nil, nil
		return nil, &sentError{err}
	}
	if _, err := k.conn.Write(header.Bytes()); err != nil {
		_ = k.conn.Close()
		k.conn, k.rd = nil, nil
		return nil, &sentError{err}
	}
	if _, err := k.conn.Write(body); err != nil {
		_ = k.conn.Close()
		k.conn, k.rd = nil, nil
		return nil, &sentError{err}
	}

	// Read response: length(int32), correlationId(int32), responseBody
	var respLen int32
	if err := binary.Read(k.rd, binary.BigEndian, &respLen); err != nil {
		_ = k.conn.Close()
		k.conn, k.rd = nil, nil
		return nil, &sentError{err}
	}

	respBytes := make([]byte, respLen)
	if _, err := io.ReadFull(k.rd, respBytes); err != nil {
		_ = k.conn.Close()
		k.conn, k.rd = nil, nil
		return nil, &sentError{err}
	}

	if len(respBytes) < 4 {
		return nil, &sentError{fmt.Errorf("kafka: response too short (%d bytes)", len(respBytes))}
	}
	respCorrID := int32(binary.BigEndian.Uint32(respBytes[:4]))
	if respCorrID != corrID {
		return nil, &sentError{fmt.Errorf("kafka: correlation ID mismatch (got %d, want %d)", respCorrID, corrID)}
	}

	return respBytes[4:], nil
}

// produce sends a message to topic partition 0.
func (k *Kafka) produce(ctx context.Context, topic string, val int64) error {
	valStr := strconv.FormatInt(val, 10)
	valBytes := []byte(valStr)

	// Build MessageSet (v0 Message)
	// Message: CRC(int32), magic(int8 0), attributes(int8 0), keyLen(-1), valLen(int32), valBytes
	var msgBody bytes.Buffer
	msgBody.WriteByte(0) // magic 0
	msgBody.WriteByte(0) // attributes 0
	_ = binary.Write(&msgBody, binary.BigEndian, int32(-1)) // key length -1 (null)
	_ = binary.Write(&msgBody, binary.BigEndian, int32(len(valBytes)))
	msgBody.Write(valBytes)

	checksum := crc32.ChecksumIEEE(msgBody.Bytes())
	var msg bytes.Buffer
	_ = binary.Write(&msg, binary.BigEndian, checksum)
	msg.Write(msgBody.Bytes())

	// MessageSet entry: offset(int64 0), messageSize(int32), message
	var msgSet bytes.Buffer
	_ = binary.Write(&msgSet, binary.BigEndian, int64(0))
	_ = binary.Write(&msgSet, binary.BigEndian, int32(msg.Len()))
	msgSet.Write(msg.Bytes())

	// Produce Request v0:
	// acks(int16 1), timeoutMs(int32 5000), topicCount(int32 1), topicName(int16 string),
	// partitionCount(int32 1), partition(int32 0), messageSetSize(int32), messageSet
	var req bytes.Buffer
	_ = binary.Write(&req, binary.BigEndian, int16(1))    // acks = 1 (leader ack)
	_ = binary.Write(&req, binary.BigEndian, int32(5000)) // timeout 5s
	_ = binary.Write(&req, binary.BigEndian, int32(1))    // 1 topic
	_ = binary.Write(&req, binary.BigEndian, int16(len(topic)))
	req.WriteString(topic)
	_ = binary.Write(&req, binary.BigEndian, int32(1)) // 1 partition
	_ = binary.Write(&req, binary.BigEndian, int32(0)) // partition 0
	_ = binary.Write(&req, binary.BigEndian, int32(msgSet.Len()))
	req.Write(msgSet.Bytes())

	resp, err := k.roundTrip(ctx, 0, 0, req.Bytes())
	if err != nil {
		return err
	}

	// Parse Produce Response v0:
	// topicCount(int32), topicName, partitionCount(int32), partition(int32), errorCode(int16), baseOffset(int64)
	r := bytes.NewReader(resp)
	var topicCount int32
	if err := binary.Read(r, binary.BigEndian, &topicCount); err != nil {
		return err
	}
	if topicCount < 1 {
		return fmt.Errorf("kafka: empty produce response")
	}
	var nameLen int16
	_ = binary.Read(r, binary.BigEndian, &nameLen)
	nameBuf := make([]byte, nameLen)
	_, _ = io.ReadFull(r, nameBuf)

	var partCount, partID int32
	var errCode int16
	var baseOffset int64
	_ = binary.Read(r, binary.BigEndian, &partCount)
	_ = binary.Read(r, binary.BigEndian, &partID)
	_ = binary.Read(r, binary.BigEndian, &errCode)
	_ = binary.Read(r, binary.BigEndian, &baseOffset)

	if errCode != 0 {
		return &kafkaError{code: errCode, msg: kafkaErrorCodeName(errCode)}
	}
	return nil
}

// fetch reads the next message from topic partition 0 starting at current offset.
func (k *Kafka) fetch(ctx context.Context, topic string) (*int64, error) {
	offset := k.offsets[topic]

	// Fetch Request v0:
	// replicaId(int32 -1), maxWaitTime(int32 500), minBytes(int32 1), topicCount(int32 1),
	// topicName(int16 string), partitionCount(int32 1), partition(int32 0), fetchOffset(int64), maxBytes(int32 65536)
	var req bytes.Buffer
	_ = binary.Write(&req, binary.BigEndian, int32(-1))   // replicaId -1 (normal consumer)
	_ = binary.Write(&req, binary.BigEndian, int32(500))  // max wait 500ms
	_ = binary.Write(&req, binary.BigEndian, int32(1))    // min bytes
	_ = binary.Write(&req, binary.BigEndian, int32(1))    // 1 topic
	_ = binary.Write(&req, binary.BigEndian, int16(len(topic)))
	req.WriteString(topic)
	_ = binary.Write(&req, binary.BigEndian, int32(1))     // 1 partition
	_ = binary.Write(&req, binary.BigEndian, int32(0))     // partition 0
	_ = binary.Write(&req, binary.BigEndian, offset)       // fetchOffset
	_ = binary.Write(&req, binary.BigEndian, int32(65536)) // maxBytes

	resp, err := k.roundTrip(ctx, 1, 0, req.Bytes())
	if err != nil {
		return nil, err
	}

	// Parse Fetch Response v0:
	// topicCount(int32), topicName, partitionCount(int32), partition(int32), errorCode(int16),
	// highWatermark(int64), messageSetSize(int32), messageSet...
	r := bytes.NewReader(resp)
	var topicCount int32
	if err := binary.Read(r, binary.BigEndian, &topicCount); err != nil {
		return nil, err
	}
	if topicCount < 1 {
		return nil, nil // empty
	}
	var nameLen int16
	_ = binary.Read(r, binary.BigEndian, &nameLen)
	nameBuf := make([]byte, nameLen)
	_, _ = io.ReadFull(r, nameBuf)

	var partCount, partID int32
	var errCode int16
	var hwm int64
	var msgSetSize int32
	_ = binary.Read(r, binary.BigEndian, &partCount)
	_ = binary.Read(r, binary.BigEndian, &partID)
	_ = binary.Read(r, binary.BigEndian, &errCode)
	_ = binary.Read(r, binary.BigEndian, &hwm)
	_ = binary.Read(r, binary.BigEndian, &msgSetSize)

	if errCode != 0 {
		return nil, &kafkaError{code: errCode, msg: kafkaErrorCodeName(errCode)}
	}
	if msgSetSize <= 0 {
		return nil, nil // no messages yet (empty queue)
	}

	// Parse first message in MessageSet
	var msgOffset int64
	var msgSize int32
	if err := binary.Read(r, binary.BigEndian, &msgOffset); err != nil {
		return nil, nil
	}
	if err := binary.Read(r, binary.BigEndian, &msgSize); err != nil || msgSize <= 0 {
		return nil, nil
	}

	// Read message body: crc(4), magic(1), attr(1), keyLen(4), valLen(4), value...
	var crc int32
	var magic, attr int8
	var keyLen, valLen int32
	_ = binary.Read(r, binary.BigEndian, &crc)
	_ = binary.Read(r, binary.BigEndian, &magic)
	_ = binary.Read(r, binary.BigEndian, &attr)
	_ = binary.Read(r, binary.BigEndian, &keyLen)
	if keyLen > 0 {
		_, _ = io.CopyN(io.Discard, r, int64(keyLen))
	}
	if err := binary.Read(r, binary.BigEndian, &valLen); err != nil || valLen <= 0 {
		k.offsets[topic] = msgOffset + 1
		return nil, nil
	}

	valBuf := make([]byte, valLen)
	if _, err := io.ReadFull(r, valBuf); err != nil {
		return nil, err
	}
	k.offsets[topic] = msgOffset + 1

	n, err := strconv.ParseInt(string(valBuf), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("kafka: non-integer message %q", string(valBuf))
	}
	return history.Int(n), nil
}

// Invoke implements Client.
func (k *Kafka) Invoke(ctx context.Context, op history.Op) history.Op {
	out, err := k.invoke(ctx, op)
	if err == nil {
		return out
	}
	t := history.Info
	var ke *kafkaError
	var se *sentError

	switch {
	case op.F == history.FRead || op.F == history.FDequeue || op.F == "pop":
		t = history.Fail // fetch errors do not mutate state
	case errors.As(err, &ke):
		switch ke.code {
		case 3, 5, 6: // UNKNOWN_TOPIC, LEADER_NOT_AVAILABLE, NOT_LEADER_OR_FOLLOWER
			t = history.Fail // rejected before leader accepted
		default:
			t = history.Fail
		}
	case !errors.As(err, &se):
		t = history.Fail // dial or local error before sending
	}

	out = complete(op, t)
	out.Error = err.Error()
	return out
}

func (k *Kafka) invoke(ctx context.Context, op history.Op) (history.Op, error) {
	topic := k.prefix + op.Key
	switch op.F {
	case history.FEnqueue, "push", history.FWrite:
		val := int64(0)
		if op.Value != nil {
			val = *op.Value
		}
		if err := k.produce(ctx, topic, val); err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil

	case history.FDequeue, "pop", history.FRead:
		val, err := k.fetch(ctx, topic)
		if err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		out.Value = val
		return out, nil
	}
	return history.Op{}, &kafkaError{code: -1, msg: "unsupported op " + op.F}
}
