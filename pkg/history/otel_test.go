package history

import (
	"strings"
	"testing"
)

func TestReadOTelResourceSpansAndAutoDetect(t *testing.T) {
	otlpJSON := `{
  "resourceSpans": [
    {
      "resource": {
        "attributes": [
          { "key": "service.name", "value": { "stringValue": "payment-gateway" } },
          { "key": "net.peer.name", "value": { "stringValue": "pg-node-1" } }
        ]
      },
      "scopeSpans": [
        {
          "spans": [
            {
              "name": "SET",
              "startTimeUnixNano": "1000000000",
              "endTimeUnixNano": "1005000000",
              "attributes": [
                { "key": "db.key", "value": { "stringValue": "acct:1" } },
                { "key": "db.value", "value": { "intValue": "100" } },
                { "key": "strife.process", "value": { "intValue": "1" } }
              ],
              "status": { "code": "STATUS_CODE_OK" }
            },
            {
              "name": "GET",
              "startTimeUnixNano": "1002000000",
              "endTimeUnixNano": "1008000000",
              "attributes": [
                { "key": "db.key", "value": { "stringValue": "acct:1" } },
                { "key": "db.result", "value": { "intValue": "100" } },
                { "key": "strife.process", "value": { "intValue": "1" } }
              ],
              "status": { "code": 1 }
            },
            {
              "name": "CAS",
              "startTimeUnixNano": "1010000000",
              "endTimeUnixNano": "1015000000",
              "attributes": [
                { "key": "strife.key", "value": { "stringValue": "acct:1" } },
                { "key": "strife.old", "value": { "intValue": "100" } },
                { "key": "strife.value", "value": { "intValue": "250" } }
              ],
              "status": { "code": 2, "message": "context deadline exceeded" }
            }
          ]
        }
      ]
    }
  ]
}`

	ops, err := ReadJSONL(strings.NewReader(otlpJSON))
	if err != nil {
		t.Fatalf("ReadJSONL auto-detect OTel failed: %v", err)
	}
	if len(ops) != 6 {
		t.Fatalf("expected 6 ops (3 spans * 2), got %d", len(ops))
	}

	paired, err := Pair(ops)
	if err != nil {
		t.Fatalf("Pair failed on concurrent OTel spans: %v", err)
	}
	if len(paired) != 3 {
		t.Fatalf("expected 3 paired operations, got %d", len(paired))
	}
	if paired[0].Input.F != FWrite || paired[0].Input.Key != "acct:1" || paired[0].Status() != OK {
		t.Fatalf("unexpected paired[0]: %+v", paired[0])
	}
	if paired[1].Input.F != FRead || paired[1].Output.Value == nil || *paired[1].Output.Value != 100 {
		t.Fatalf("unexpected paired[1]: %+v", paired[1])
	}
	// Span 3 had deadline exceeded -> Info
	if paired[2].Input.F != FCas || paired[2].Status() != Info {
		t.Fatalf("expected paired[2] to be CAS with Info status, got %+v", paired[2])
	}
}

func TestReadOTelTransactions(t *testing.T) {
	spanListJSON := `[
  {
    "name": "BEGIN",
    "startTimeUnixNano": 2000000,
    "endTimeUnixNano": 2500000,
    "attributes": [
      { "key": "strife.txn", "value": { "stringValue": "[{\"f\":\"append\",\"key\":\"x\",\"value\":1},{\"f\":\"r\",\"key\":\"y\",\"values\":[1,2]}]" } }
    ],
    "status": { "code": "OK" }
  }
]`
	ops, err := ReadJSONL(strings.NewReader(spanListJSON))
	if err != nil {
		t.Fatalf("ReadJSONL span array: %v", err)
	}
	paired, err := Pair(ops)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if len(paired) != 1 || paired[0].Input.F != FTxn {
		t.Fatalf("expected 1 txn op, got %+v", paired)
	}
	micro := paired[0].Micro()
	if len(micro) != 2 || micro[0].F != MicroAppend || len(micro[1].Values) != 2 {
		t.Fatalf("unexpected micro-ops: %+v", micro)
	}
}
