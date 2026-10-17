package schemas_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/internal/jsonschema"
	"github.com/kemalhafizh/strife/pkg/compliance"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/report"
)

// The files in this directory are Strife's output contracts. Run
//
//	go test ./schemas -update
//
// after an additive change (a new optional field) to record it. A change
// that removes, renames or retypes a field, or makes one required, fails
// TestCompatible: it needs a new major version (report.v2.json) instead.
var update = flag.Bool("update", false, "rewrite the schema files from the Go types")

var contracts = []struct {
	file, id, title string
	typ             reflect.Type
}{
	{"report.v1.json", "https://strife.dev/schemas/report.v1.json", "Strife report (report.json)", reflect.TypeOf(report.Report{})},
	{"history.v1.json", "https://strife.dev/schemas/history.v1.json", "One line of a Strife history (history.jsonl)", reflect.TypeOf(history.Op{})},
	{"dossier.v1.json", "https://strife.dev/schemas/dossier.v1.json", "Strife compliance dossier (dossier.json)", reflect.TypeOf(compliance.Dossier{})},
}

func load(t *testing.T, file string) jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, data).(map[string]any)
}

func decode(t *testing.T, data []byte) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCompatible(t *testing.T) {
	for _, c := range contracts {
		gen := jsonschema.Generate(c.typ, c.id, c.title)
		data, err := json.MarshalIndent(gen, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		if *update {
			if old, err := os.ReadFile(c.file); err == nil {
				if breaks := jsonschema.Compatible(decode(t, old).(map[string]any), decode(t, data).(map[string]any)); len(breaks) > 0 {
					t.Fatalf("%s: refusing to record a breaking change: %v", c.file, breaks)
				}
			}
			if err := os.WriteFile(c.file, data, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		committed := load(t, c.file)
		if breaks := jsonschema.Compatible(committed, decode(t, data).(map[string]any)); len(breaks) > 0 {
			t.Errorf("%s: the Go types break the published contract: %v", c.file, breaks)
		}
		old, _ := os.ReadFile(c.file)
		if !bytes.Equal(old, data) {
			t.Errorf("%s is out of date with the Go types: run go test ./schemas -update", c.file)
		}
	}
}

// TestRealOutputsValidate validates what Strife actually writes.
func TestRealOutputsValidate(t *testing.T) {
	cfg := explore.DefaultConfig()
	cfg.System, cfg.Seed, cfg.Duration = "raft-stale-reads", 2, 10*time.Second
	res, err := explore.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := report.New(report.KindSimulation, cfg.System, cfg.Seed, res.Config, res.Schedule, res.History, res.Check, res.TraceHash)
	if err != nil {
		t.Fatal(err)
	}
	rep.Applied = []report.Applied{{FaultID: 1, Kind: "partition", Start: true, Landed: 5, Latency: 1}}
	check := func(file string, v any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := jsonschema.Validate(load(t, file), decode(t, data)); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
	check("report.v1.json", rep)
	for _, op := range res.History[:min(200, len(res.History))] {
		check("history.v1.json", op)
	}
	check("history.v1.json", history.Op{Process: 1, Type: history.OK, F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroRead, Key: "k", Values: []int64{1, 2}}}})
	d, err := compliance.Evaluate(compliance.Options{Report: rep, Now: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	check("dossier.v1.json", d)
}
