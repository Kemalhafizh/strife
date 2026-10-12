package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Etcd speaks to etcd's v3 gRPC-gateway JSON API, so it needs nothing but the
// standard library. Reads use the default (linearizable) consistency; cas is
// a transaction comparing the key's value.
type Etcd struct {
	base   string
	prefix string
	http   *http.Client
}

// NewEtcd returns a client for one etcd endpoint, e.g. http://10.0.0.1:2379.
func NewEtcd(endpoint, prefix string) *Etcd {
	return &Etcd{
		base:   strings.TrimRight(endpoint, "/"),
		prefix: prefix,
		http:   &http.Client{Transport: &http.Transport{DisableKeepAlives: false, MaxIdleConnsPerHost: 4}},
	}
}

// Close implements Client.
func (e *Etcd) Close() error {
	e.http.CloseIdleConnections()
	return nil
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

type etcdKV struct {
	Value string `json:"value"`
}

func (e *Etcd) post(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return json.Unmarshal(data, out)
}

type statusError struct {
	code int
	body string
}

func (s *statusError) Error() string { return fmt.Sprintf("http %d: %s", s.code, s.body) }

// Invoke implements Client.
func (e *Etcd) Invoke(ctx context.Context, op history.Op) history.Op {
	out, err := e.invoke(ctx, op)
	if err != nil {
		t := Classify(op, err)
		// A 4xx means the request was rejected before being applied.
		if se, ok := err.(*statusError); ok && se.code >= 400 && se.code < 500 {
			t = history.Fail
		}
		out = complete(op, t)
		out.Error = err.Error()
	}
	return out
}

func (e *Etcd) invoke(ctx context.Context, op history.Op) (history.Op, error) {
	key := b64(e.prefix + op.Key)
	switch op.F {
	case history.FRead:
		var resp struct {
			Kvs []etcdKV `json:"kvs"`
		}
		if err := e.post(ctx, "/v3/kv/range", map[string]any{"key": key}, &resp); err != nil {
			return history.Op{}, err
		}
		out := complete(op, history.OK)
		if len(resp.Kvs) > 0 {
			raw, err := base64.StdEncoding.DecodeString(resp.Kvs[0].Value)
			if err != nil {
				return history.Op{}, err
			}
			v, err := strconv.ParseInt(string(raw), 10, 64)
			if err != nil {
				return history.Op{}, fmt.Errorf("non-integer value %q", raw)
			}
			out.Value = history.Int(v)
		}
		return out, nil
	case history.FWrite:
		var resp map[string]any
		body := map[string]any{"key": key, "value": b64(strconv.FormatInt(*op.Value, 10))}
		if err := e.post(ctx, "/v3/kv/put", body, &resp); err != nil {
			return history.Op{}, err
		}
		return complete(op, history.OK), nil
	case history.FCas:
		var resp struct {
			Succeeded bool `json:"succeeded"`
		}
		body := map[string]any{
			"compare": []map[string]any{{
				"key": key, "target": "VALUE", "result": "EQUAL",
				"value": b64(strconv.FormatInt(*op.Old, 10)),
			}},
			"success": []map[string]any{{
				"requestPut": map[string]any{"key": key, "value": b64(strconv.FormatInt(*op.Value, 10))},
			}},
		}
		if err := e.post(ctx, "/v3/kv/txn", body, &resp); err != nil {
			return history.Op{}, err
		}
		if resp.Succeeded {
			return complete(op, history.OK), nil
		}
		return complete(op, history.Fail), nil
	}
	return history.Op{}, &statusError{code: 400, body: "unsupported op " + op.F}
}
