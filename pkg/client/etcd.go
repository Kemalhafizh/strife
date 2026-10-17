package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	case history.FTxn:
		return e.txn(ctx, op)
	}
	return history.Op{}, &statusError{code: 400, body: "unsupported op " + op.F}
}

// txn runs a list-append transaction optimistically: read every key in one
// etcd transaction, compute the result, then commit the new lists in a
// transaction that compares every key's mod revision with what was read. A
// lost race changes nothing and is retried; if every attempt loses, the
// operation definitely failed.
func (e *Etcd) txn(ctx context.Context, op history.Op) (history.Op, error) {
	var keys []string
	for _, m := range op.Txn {
		if !slices.Contains(keys, m.Key) {
			keys = append(keys, m.Key)
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		var snap struct {
			Responses []struct {
				Range struct {
					Kvs []struct {
						Value       string `json:"value"`
						ModRevision string `json:"mod_revision"`
					} `json:"kvs"`
				} `json:"response_range"`
			} `json:"responses"`
		}
		var ranges []map[string]any
		for _, k := range keys {
			ranges = append(ranges, map[string]any{"requestRange": map[string]any{"key": b64(e.prefix + k)}})
		}
		if err := e.post(ctx, "/v3/kv/txn", map[string]any{"success": ranges}, &snap); err != nil {
			if attempt > 0 {
				err = &statusError{code: 409, body: "commit retry: " + err.Error()} // nothing committed yet
			}
			return history.Op{}, err
		}
		if len(snap.Responses) != len(keys) {
			return history.Op{}, &statusError{code: 502, body: "etcd: short txn response"}
		}
		lists := map[string][]int64{}
		revs := map[string]string{}
		for i, k := range keys {
			revs[k] = "0"
			if kvs := snap.Responses[i].Range.Kvs; len(kvs) > 0 {
				raw, err := base64.StdEncoding.DecodeString(kvs[0].Value)
				if err != nil {
					return history.Op{}, err
				}
				if lists[k], err = parseList(string(raw)); err != nil {
					return history.Op{}, err
				}
				revs[k] = kvs[0].ModRevision
			}
		}
		out := complete(op, history.OK)
		written := false
		for i, m := range op.Txn {
			switch m.F {
			case history.MicroAppend:
				lists[m.Key] = append(lists[m.Key], *m.Value)
				written = true
			case history.MicroRead:
				out.Txn[i].Values = append([]int64{}, lists[m.Key]...)
			}
		}
		if !written {
			return out, nil // one atomic, linearizable snapshot
		}
		var compare, puts []map[string]any
		for _, k := range keys {
			compare = append(compare, map[string]any{"key": b64(e.prefix + k), "target": "MOD", "result": "EQUAL", "mod_revision": revs[k]})
		}
		for _, k := range keys {
			if !slices.ContainsFunc(op.Txn, func(m history.MicroOp) bool { return m.Key == k && m.F == history.MicroAppend }) {
				continue
			}
			strs := make([]string, len(lists[k]))
			for i, v := range lists[k] {
				strs[i] = strconv.FormatInt(v, 10)
			}
			puts = append(puts, map[string]any{"requestPut": map[string]any{"key": b64(e.prefix + k), "value": b64(strings.Join(strs, ","))}})
		}
		var resp struct {
			Succeeded bool `json:"succeeded"`
		}
		if err := e.post(ctx, "/v3/kv/txn", map[string]any{"compare": compare, "success": puts}, &resp); err != nil {
			return history.Op{}, err
		}
		if resp.Succeeded {
			return out, nil
		}
	}
	return history.Op{}, &statusError{code: 409, body: "etcd: lost every optimistic commit race"}
}
