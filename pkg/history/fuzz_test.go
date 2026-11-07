package history

import (
	"bytes"
	"testing"
)

func FuzzReadJSONL(f *testing.F) {
	f.Add([]byte(`{"index":0,"process":0,"type":"invoke","f":"write","key":"x","value":1,"time":1}
{"index":1,"process":0,"type":"ok","f":"write","key":"x","value":1,"time":2}
`))
	f.Add([]byte(`{"process":3,"type":"ok","f":"txn","txn":[{"f":"r","key":"k","values":[1,2]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := ReadJSONL(bytes.NewReader(data))
		if err != nil {
			return
		}
		paired, err := Pair(ops)
		if err != nil {
			return
		}
		for _, o := range paired {
			_ = o.Describe()
			_ = o.Micro()
		}
		_ = Summarize(paired)
	})
}

func FuzzReadEDN(f *testing.F) {
	f.Add([]byte(`{:type :invoke, :f :write, :key "k0", :value 1, :process 0, :time 10}
{:type :ok, :f :write, :key "k0", :value 1, :process 0, :time 20}
{:type :invoke, :f :read, :key "k0", :process 1, :time 30}
{:type :ok, :f :read, :key "k0", :value 1, :process 1, :time 40}
`))
	f.Add([]byte(`[{:type :invoke, :f :txn, :value [[:append "x" 1] [:r "y" nil]], :process 2, :time 100}
{:type :ok, :f :txn, :value [[:append "x" 1] [:r "y" [1 2]]], :process 2, :time 200}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := ReadEDN(bytes.NewReader(data))
		if err != nil {
			return
		}
		paired, err := Pair(ops)
		if err != nil {
			return
		}
		for _, o := range paired {
			_ = o.Describe()
			_ = o.Micro()
		}
		_ = Summarize(paired)
	})
}
