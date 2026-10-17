package jsonschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inner struct {
	N int `json:"n"`
}

type doc struct {
	Name    string            `json:"name"`
	Opt     string            `json:"opt,omitempty"`
	Ptr     *int64            `json:"ptr"`
	Wait    time.Duration     `json:"wait"`
	Items   []inner           `json:"items"`
	Labels  map[string]string `json:"labels,omitempty"`
	Raw     json.RawMessage   `json:"raw,omitempty"`
	Skipped string            `json:"-"`
	Self    *doc              `json:"self,omitempty"`
}

func decode(t *testing.T, s string) any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGenerateAndValidate(t *testing.T) {
	s := Generate(reflect.TypeOf(doc{}), "x", "doc")
	good := `{"name":"a","ptr":null,"wait":5,"items":[{"n":1}],"labels":{"k":"v"},"raw":{"any":[1]},"self":{"name":"b","ptr":3,"wait":0,"items":null}}`
	if err := Validate(s, decode(t, good)); err != nil {
		t.Fatal(err)
	}
	for bad, want := range map[string]string{
		`{"ptr":null,"wait":5,"items":[]}`:                         `missing required "name"`,
		`{"name":"a","ptr":null,"wait":5.5,"items":[]}`:            "not of type",
		`{"name":"a","ptr":null,"wait":5,"items":[{"n":"x"}]}`:     "$.items[0].n",
		`{"name":"a","ptr":null,"wait":5,"items":[],"extra":true}`: `unexpected property "extra"`,
	} {
		if err := Validate(s, decode(t, bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", bad, err, want)
		}
	}
}

func TestCompatible(t *testing.T) {
	type v1 struct {
		A string `json:"a"`
		B int    `json:"b,omitempty"`
	}
	type v2add struct {
		A string `json:"a"`
		B int    `json:"b,omitempty"`
		C bool   `json:"c,omitempty"`
	}
	type v2break struct {
		A int  `json:"a"`
		C bool `json:"c"`
	}
	base := Generate(reflect.TypeOf(v1{}), "x", "t")
	if got := Compatible(base, Generate(reflect.TypeOf(v2add{}), "x", "t")); len(got) != 0 {
		t.Fatalf("adding an optional field is compatible: %v", got)
	}
	got := strings.Join(Compatible(base, Generate(reflect.TypeOf(v2break{}), "x", "t")), "; ")
	for _, want := range []string{"$.a: type changed", "$.b: removed", "$.c: newly required"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}
