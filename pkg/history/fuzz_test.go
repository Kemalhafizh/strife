package history

import (
	"bytes"
	"testing"
)

func exerciseHistoryOps(ops []Op) {
	paired, err := Pair(ops)
	if err != nil {
		return
	}
	for _, o := range paired {
		_ = o.Describe()
		_ = o.Micro()
	}
	_ = Summarize(paired)
}

func FuzzReadJSONL(f *testing.F) {
	f.Add([]byte(`{"index":0,"process":0,"type":"invoke","f":"write","key":"x","value":1,"time":1}
{"index":1,"process":0,"type":"ok","f":"write","key":"x","value":1,"time":2}
`))
	f.Add([]byte(`{"process":3,"type":"ok","f":"txn","txn":[{"f":"r","key":"k","values":[1,2]}]}`))
	f.Add([]byte(`{:type :invoke, :f :write, :value [:x 1], :process 0, :time 100}
{:type :ok, :f :write, :value [:x 1], :process 0, :time 200}
`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"spanId":"s1","name":"write","startTimeUnixNano":"1000","endTimeUnixNano":"2000","attributes":[{"key":"strife.f","value":{"stringValue":"write"}},{"key":"strife.key","value":{"stringValue":"k0"}},{"key":"strife.value","value":{"intValue":"42"}}]}]}]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := ReadJSONL(bytes.NewReader(data))
		if err != nil {
			return
		}
		exerciseHistoryOps(ops)
	})
}

func FuzzReadEDN(f *testing.F) {
	f.Add([]byte(`{:type :invoke, :f :write, :value ["k0" 10], :process 1, :time 1000}
{:type :ok, :f :write, :value ["k0" 10], :process 1, :time 2000}
{:type :invoke, :f :cas, :value ["k0" [10 20]], :process 2, :time 2500}
{:type :ok, :f :cas, :value ["k0" [10 20]], :process 2, :time 3000}
{:type :invoke, :f :read, :value ["k0" nil], :process 1, :time 3100}
{:type :ok, :f :read, :value ["k0" 20], :process 1, :time 3500}
`))
	f.Add([]byte(`; Jepsen Elle transactional history
{:type :invoke, :f :txn, :value [[:r 1 nil] [:append 2 9]], :process 0, :time 100}
{:type :ok, :f :txn, :value [[:r 1 [3 4]] [:append 2 9]], :process 0, :time 200}
{:type :invoke, :f :transfer, :value ["acct:0->acct:1" 50], :process 3, :time 300}
{:type :info, :f :transfer, :value ["acct:0->acct:1" 50], :process 3, :time 900, :error :timeout}
`))
	f.Add([]byte(`[{:type :invoke, :f :txn, :value [[:append "x" 1] [:r "y" nil]], :process 2, :time 100}
{:type :ok, :f :txn, :value [[:append "x" 1] [:r "y" [1 2]]], :process 2, :time 200}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := ReadEDN(bytes.NewReader(data))
		if err != nil {
			return
		}
		exerciseHistoryOps(ops)
	})
}

func FuzzReadOTel(f *testing.F) {
	f.Add([]byte(`{
  "resourceSpans": [{
    "resource": {"attributes": [{"key": "process.pid", "value": {"intValue": "1"}}]},
    "scopeSpans": [{
      "spans": [
        {
          "spanId": "a1",
          "name": "kv.write",
          "startTimeUnixNano": "1000000",
          "endTimeUnixNano": "2000000",
          "attributes": [
            {"key": "strife.f", "value": {"stringValue": "write"}},
            {"key": "strife.key", "value": {"stringValue": "k0"}},
            {"key": "strife.value", "value": {"intValue": "7"}}
          ],
          "status": {"code": "STATUS_CODE_OK"}
        },
        {
          "spanId": "a2",
          "name": "kv.cas",
          "startTimeUnixNano": "1500000",
          "endTimeUnixNano": "2500000",
          "attributes": [
            {"key": "strife.f", "value": {"stringValue": "cas"}},
            {"key": "strife.key", "value": {"stringValue": "k0"}},
            {"key": "strife.expected", "value": {"intValue": "7"}},
            {"key": "strife.value", "value": {"intValue": "8"}}
          ],
          "status": {"code": 1}
        }
      ]
    }]
  }]
}`))
	f.Add([]byte(`{
  "data": [{
    "traceID": "t1",
    "spans": [{
      "spanID": "s1",
      "operationName": "txn",
      "startTime": 1000,
      "duration": 500,
      "tags": [
        {"key": "db.statement", "value": "UPDATE kv SET v = 10 WHERE k = 'acct:0'; SELECT v FROM kv WHERE k = 'acct:1'"}
      ]
    }]
  }]
}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := ReadOTel(bytes.NewReader(data))
		if err != nil {
			return
		}
		exerciseHistoryOps(ops)
	})
}
