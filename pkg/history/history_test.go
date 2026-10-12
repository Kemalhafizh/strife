package history

import (
	"bytes"
	"testing"
)

func sample() []Op {
	r := NewRecorder(func() int64 { return 0 })
	r.Record(Op{Process: 0, Type: Invoke, F: FWrite, Key: "x", Value: Int(1)})
	r.Record(Op{Process: 1, Type: Invoke, F: FRead, Key: "x"})
	r.Record(Op{Process: 0, Type: OK, F: FWrite, Key: "x", Value: Int(1)})
	r.Record(Op{Process: 1, Type: OK, F: FRead, Key: "x", Value: Int(1)})
	r.Record(Op{Process: 2, Type: Invoke, F: FCas, Key: "x", Old: Int(1), Value: Int(2)})
	return r.Ops()
}

func TestJSONLRoundTrip(t *testing.T) {
	ops := sample()
	var buf bytes.Buffer
	if err := WriteJSONL(&buf, ops); err != nil {
		t.Fatal(err)
	}
	back, err := ReadJSONL(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if Hash(back) != Hash(ops) {
		t.Fatal("round trip changed the history")
	}
}

func TestPair(t *testing.T) {
	ops, err := Pair(sample())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("want 3 operations, got %d", len(ops))
	}
	if ops[2].Status() != Info || ops[2].Return != Infinity {
		t.Fatalf("unfinished op should be info with infinite return: %+v", ops[2])
	}
	if *ops[1].Output.Value != 1 {
		t.Fatal("read result lost")
	}
	s := Summarize(ops)
	if s.OK != 2 || s.Info != 1 || s.ByF[FRead].OK != 1 {
		t.Fatalf("bad stats %+v", s)
	}
}

func TestPairRejectsMalformed(t *testing.T) {
	bad := [][]Op{
		{{Process: 0, Type: Invoke, F: FRead}, {Process: 0, Type: Invoke, F: FRead}},
		{{Process: 0, Type: OK, F: FRead}},
		{{Process: 0, Type: "maybe", F: FRead}},
	}
	for i, h := range bad {
		if _, err := Pair(h); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestReadJSONLSkipsCommentsAndSortsByTime(t *testing.T) {
	in := `# merged from two clients
{"process":1,"type":"invoke","f":"read","key":"x","time":20}
{"process":0,"type":"invoke","f":"write","key":"x","value":3,"time":10}

{"process":0,"type":"ok","f":"write","key":"x","value":3,"time":15}
{"process":1,"type":"ok","f":"read","key":"x","value":3,"time":25}
`
	ops, err := ReadJSONL(bytes.NewBufferString(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 4 || ops[0].Time != 10 || ops[0].Index != 0 || ops[3].Index != 3 {
		t.Fatalf("got %+v", ops)
	}
}
