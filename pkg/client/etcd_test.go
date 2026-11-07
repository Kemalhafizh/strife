package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kemalhafizh/strife/pkg/history"
)

func TestEtcdLedgerTransactions(t *testing.T) {
	balances := map[string]string{}
	revs := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/kv/txn" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var body struct {
			Compare []struct {
				Key         string `json:"key"`
				ModRevision string `json:"mod_revision"`
			} `json:"compare"`
			Success []struct {
				RequestRange *struct {
					Key string `json:"key"`
				} `json:"requestRange"`
				RequestPut *struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"requestPut"`
			} `json:"success"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Compare) == 0 {
			// Snapshot read of accounts.
			type kvItem struct {
				Value       string `json:"value"`
				ModRevision string `json:"mod_revision"`
			}
			type rangeResp struct {
				Range struct {
					Kvs []kvItem `json:"kvs"`
				} `json:"response_range"`
			}
			resp := struct {
				Responses []rangeResp `json:"responses"`
			}{}
			for _, op := range body.Success {
				var rr rangeResp
				if v, ok := balances[op.RequestRange.Key]; ok {
					rr.Range.Kvs = []kvItem{{Value: v, ModRevision: revs[op.RequestRange.Key]}}
				}
				resp.Responses = append(resp.Responses, rr)
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Compare-and-swap commit.
		for _, c := range body.Compare {
			cur := revs[c.Key]
			if cur == "" {
				cur = "0"
			}
			if cur != c.ModRevision {
				_ = json.NewEncoder(w).Encode(map[string]any{"succeeded": false})
				return
			}
		}
		for _, op := range body.Success {
			balances[op.RequestPut.Key] = op.RequestPut.Value
			revs[op.RequestPut.Key] = "2"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"succeeded": true})
	}))
	defer srv.Close()

	e := NewEtcd(srv.URL, "p/")
	defer e.Close()

	transfer := history.Op{
		F:     history.FTxn,
		Key:   "acct:0->acct:1",
		Value: history.Int(400),
		Txn: []history.MicroOp{
			{F: history.MicroRead, Key: "acct:0"},
			{F: history.MicroWrite, Key: "acct:0"},
			{F: history.MicroRead, Key: "acct:1"},
			{F: history.MicroWrite, Key: "acct:1"},
		},
	}
	out := e.Invoke(context.Background(), transfer)
	if out.Type != history.OK || len(out.Txn) != 4 || *out.Txn[1].Value != 600 || *out.Txn[3].Value != 1400 {
		t.Fatalf("transfer ok: %+v", out)
	}

	overdraft := transfer
	overdraft.Value = history.Int(800)
	if out := e.Invoke(context.Background(), overdraft); out.Type != history.Fail {
		t.Fatalf("overdraft must fail, got %+v", out)
	}

	audit := history.Op{
		F:   history.FTxn,
		Key: "audit",
		Txn: []history.MicroOp{
			{F: history.MicroRead, Key: "acct:0"},
			{F: history.MicroRead, Key: "acct:1"},
			{F: history.MicroRead, Key: "acct:2"},
		},
	}
	out = e.Invoke(context.Background(), audit)
	if out.Type != history.OK || len(out.Txn) != 3 || *out.Txn[0].Value != 600 || *out.Txn[1].Value != 1400 || *out.Txn[2].Value != 1000 {
		t.Fatalf("audit: %+v", out)
	}
}
