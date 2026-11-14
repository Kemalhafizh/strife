package history

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ReadOTel parses an OpenTelemetry trace JSON payload (OTLP resourceSpans,
// Jaeger trace JSON, or a JSON span list) into a chronological Strife history.
// Each client/database span is converted into an Invoke event at its start
// timestamp and a completion event (OK, Fail, or Info) at its end timestamp.
func ReadOTel(r io.Reader) ([]Op, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	spans, err := extractOTelSpans(raw)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		return nil, nil
	}

	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].startNs != spans[j].startNs {
			return spans[i].startNs < spans[j].startNs
		}
		return spans[i].endNs < spans[j].endNs
	})

	baseNs := spans[0].startNs
	if baseNs < 0 {
		baseNs = 0
	}

	// Track when each process lane becomes free so concurrent spans never
	// collide on the same process ID during history.Pair.
	busyUntil := make(map[int]int64)

	allocProc := func(preferred int, hasPref bool, startRel, endRel int64, status Type) int {
		if hasPref && preferred > 0 {
			if until, exists := busyUntil[preferred]; !exists || until < startRel {
				if status == Info {
					busyUntil[preferred] = math.MaxInt64
				} else {
					busyUntil[preferred] = endRel
				}
				return preferred
			}
		}
		for p := 1; ; p++ {
			if until, exists := busyUntil[p]; !exists || until < startRel {
				if status == Info {
					busyUntil[p] = math.MaxInt64
				} else {
					busyUntil[p] = endRel
				}
				return p
			}
		}
	}

	ops := make([]Op, 0, len(spans)*2)
	for _, sp := range spans {
		startRel := sp.startNs - baseNs
		endRel := sp.endNs - baseNs
		if endRel <= startRel {
			endRel = startRel + 1
		}
		proc := allocProc(sp.proc, sp.hasProc, startRel, endRel, sp.status)

		inv := Op{
			Process: proc,
			Type:    Invoke,
			F:       sp.f,
			Key:     sp.key,
			Value:   sp.inVal,
			Old:     sp.oldVal,
			Txn:     sp.inTxn,
			Time:    startRel,
			Node:    sp.node,
		}
		comp := Op{
			Process: proc,
			Type:    sp.status,
			F:       sp.f,
			Key:     sp.key,
			Value:   sp.outVal,
			Old:     sp.oldVal,
			Values:  sp.values,
			Txn:     sp.outTxn,
			Time:    endRel,
			Node:    sp.node,
			Error:   sp.errMsg,
		}
		ops = append(ops, inv, comp)
	}

	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].Time != ops[j].Time {
			return ops[i].Time < ops[j].Time
		}
		if ops[i].Process == ops[j].Process && ops[i].Type != ops[j].Type {
			return ops[i].Type == Invoke
		}
		return false
	})
	for i := range ops {
		ops[i].Index = i
	}
	return ops, nil
}

type parsedSpan struct {
	startNs int64
	endNs   int64
	proc    int
	hasProc bool
	f       string
	key     string
	inVal   *int64
	outVal  *int64
	oldVal  *int64
	values  []int64
	inTxn   []MicroOp
	outTxn  []MicroOp
	status  Type
	node    string
	errMsg  string
}

type otlpDoc struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otelKeyValue `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []rawOTelSpan `json:"spans"`
		} `json:"scopeSpans"`
		InstSpans []struct {
			Spans []rawOTelSpan `json:"spans"`
		} `json:"instrumentationLibrarySpans"`
	} `json:"resourceSpans"`
	Spans []rawOTelSpan `json:"spans"`
	Data  []struct {
		Spans []rawOTelSpan `json:"spans"`
	} `json:"data"`
}

type rawOTelSpan struct {
	Name              string          `json:"name"`
	OperationName     string          `json:"operationName"`
	StartTimeUnixNano json.RawMessage `json:"startTimeUnixNano"`
	EndTimeUnixNano   json.RawMessage `json:"endTimeUnixNano"`
	StartTimeMicros   int64           `json:"startTime"`
	DurationMicros    int64           `json:"duration"`
	Attributes        []otelKeyValue  `json:"attributes"`
	Tags              []otelKeyValue  `json:"tags"`
	Status            struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"status"`
}

type otelKeyValue struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func extractOTelSpans(raw []byte) ([]parsedSpan, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	var out []parsedSpan
	if strings.HasPrefix(trimmed, "[") {
		var list []rawOTelSpan
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("history: otel span array: %w", err)
		}
		for _, sp := range list {
			if ps, ok := convertOTelSpan(nil, sp); ok {
				out = append(out, ps)
			}
		}
		return out, nil
	}

	var doc otlpDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("history: otel json: %w", err)
	}
	for _, rs := range doc.ResourceSpans {
		resAttrs := parseOTelAttrs(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if ps, ok := convertOTelSpan(resAttrs, sp); ok {
					out = append(out, ps)
				}
			}
		}
		for _, is := range rs.InstSpans {
			for _, sp := range is.Spans {
				if ps, ok := convertOTelSpan(resAttrs, sp); ok {
					out = append(out, ps)
				}
			}
		}
	}
	for _, sp := range doc.Spans {
		if ps, ok := convertOTelSpan(nil, sp); ok {
			out = append(out, ps)
		}
	}
	for _, d := range doc.Data {
		for _, sp := range d.Spans {
			if ps, ok := convertOTelSpan(nil, sp); ok {
				out = append(out, ps)
			}
		}
	}
	return out, nil
}

func convertOTelSpan(resAttrs map[string]string, sp rawOTelSpan) (parsedSpan, bool) {
	attrs := make(map[string]string, len(resAttrs)+len(sp.Attributes)+len(sp.Tags))
	for k, v := range resAttrs {
		attrs[k] = v
	}
	for k, v := range parseOTelAttrs(sp.Attributes) {
		attrs[k] = v
	}
	for k, v := range parseOTelAttrs(sp.Tags) {
		attrs[k] = v
	}

	startNs := parseRawNano(sp.StartTimeUnixNano)
	endNs := parseRawNano(sp.EndTimeUnixNano)
	if startNs == 0 && sp.StartTimeMicros > 0 {
		startNs = sp.StartTimeMicros * 1000
		endNs = (sp.StartTimeMicros + sp.DurationMicros) * 1000
	}

	name := sp.Name
	if name == "" {
		name = sp.OperationName
	}
	fRaw := firstAttr(attrs, "strife.f", "db.operation", "messaging.operation")
	if fRaw == "" {
		fRaw = inferOpFromName(name, attrs["db.statement"])
	}
	f := normalizeEdnF(fRaw)
	if f == "" {
		return parsedSpan{}, false
	}

	key := firstAttr(attrs, "strife.key", "db.key", "db.sql.table", "messaging.destination.name")
	node := firstAttr(attrs, "strife.node", "net.peer.name", "server.address", "db.instance")

	var proc int
	var hasProc bool
	if pStr := firstAttr(attrs, "strife.process", "process.id", "thread.id"); pStr != "" {
		if n, err := strconv.Atoi(pStr); err == nil {
			proc = n
			hasProc = true
		}
	}

	var inVal, outVal, oldVal *int64
	if vStr := firstAttr(attrs, "strife.value", "db.value"); vStr != "" {
		if v, err := strconv.ParseInt(vStr, 10, 64); err == nil {
			inVal = Int(v)
			outVal = Int(v)
		}
	}
	if rStr := firstAttr(attrs, "strife.result", "db.result"); rStr != "" {
		if v, err := strconv.ParseInt(rStr, 10, 64); err == nil {
			outVal = Int(v)
		}
	}
	if f == FRead || f == FDequeue || f == FGenerate {
		inVal = nil
	}
	if oStr := firstAttr(attrs, "strife.old", "db.old_value"); oStr != "" {
		if v, err := strconv.ParseInt(oStr, 10, 64); err == nil {
			oldVal = Int(v)
		}
	}

	var values []int64
	if vsStr := firstAttr(attrs, "strife.values", "db.values"); vsStr != "" {
		values = parseIntList(vsStr)
	}

	var inTxn, outTxn []MicroOp
	if txnStr := firstAttr(attrs, "strife.txn", "db.txn"); txnStr != "" {
		micros := parseOTelMicroOps(txnStr)
		if len(micros) > 0 {
			f = FTxn
			outTxn = micros
			inTxn = make([]MicroOp, len(micros))
			for i, m := range micros {
				inTxn[i] = m
				if m.F == MicroRead {
					inTxn[i].Value = nil
					inTxn[i].Values = nil
				}
			}
		}
	}

	status, errMsg := resolveOTelStatus(attrs, sp.Status.Code, sp.Status.Message, endNs)
	return parsedSpan{
		startNs: startNs,
		endNs:   endNs,
		proc:    proc,
		hasProc: hasProc,
		f:       f,
		key:     key,
		inVal:   inVal,
		outVal:  outVal,
		oldVal:  oldVal,
		values:  values,
		inTxn:   inTxn,
		outTxn:  outTxn,
		status:  status,
		node:    node,
		errMsg:  errMsg,
	}, true
}

func resolveOTelStatus(attrs map[string]string, rawCode json.RawMessage, statusMsg string, endNs int64) (Type, string) {
	errMsg := firstAttr(attrs, "strife.error", "error.message", "exception.message", "db.error")
	if errMsg == "" {
		errMsg = statusMsg
	}
	if st := strings.ToLower(strings.TrimSpace(attrs["strife.status"])); st != "" {
		switch st {
		case "ok":
			return OK, errMsg
		case "fail", "failed", "error":
			return Fail, errMsg
		case "info", "indeterminate", "timeout", "unknown":
			return Info, errMsg
		}
	}
	if endNs == 0 {
		return Info, errMsg
	}
	codeStr := strings.ToUpper(strings.Trim(string(rawCode), `"`))
	isErr := codeStr == "2" || codeStr == "STATUS_CODE_ERROR" || codeStr == "ERROR" || strings.EqualFold(attrs["error"], "true")
	if !isErr {
		return OK, ""
	}
	lowerErr := strings.ToLower(errMsg + " " + attrs["error.type"])
	if strings.Contains(lowerErr, "timeout") || strings.Contains(lowerErr, "deadline") ||
		strings.Contains(lowerErr, "reset") || strings.Contains(lowerErr, "eof") ||
		strings.Contains(lowerErr, "indeterminate") || strings.Contains(lowerErr, "unknown") {
		return Info, errMsg
	}
	return Fail, errMsg
}

func parseOTelAttrs(kvs []otelKeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv.Key == "" || len(kv.Value) == 0 {
			continue
		}
		out[kv.Key] = decodeOTelAnyValue(kv.Value)
	}
	return out
}

func decodeOTelAnyValue(raw json.RawMessage) string {
	var wrapper struct {
		StringValue *string         `json:"stringValue"`
		IntValue    json.RawMessage `json:"intValue"`
		BoolValue   *bool           `json:"boolValue"`
		ArrayValue  *struct {
			Values []json.RawMessage `json:"values"`
		} `json:"arrayValue"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		if wrapper.StringValue != nil {
			return *wrapper.StringValue
		}
		if len(wrapper.IntValue) > 0 && string(wrapper.IntValue) != "null" {
			return strings.Trim(string(wrapper.IntValue), `"`)
		}
		if wrapper.BoolValue != nil {
			return strconv.FormatBool(*wrapper.BoolValue)
		}
		if wrapper.ArrayValue != nil {
			parts := make([]string, 0, len(wrapper.ArrayValue.Values))
			for _, elem := range wrapper.ArrayValue.Values {
				parts = append(parts, decodeOTelAnyValue(elem))
			}
			return "[" + strings.Join(parts, ",") + "]"
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func parseRawNano(raw json.RawMessage) int64 {
	s := strings.TrimSpace(strings.Trim(string(raw), `"`))
	if s == "" || s == "null" {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func firstAttr(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(attrs[k]); v != "" {
			return v
		}
	}
	return ""
}

func inferOpFromName(name, stmt string) string {
	for _, src := range []string{name, stmt} {
		u := strings.ToLower(strings.TrimSpace(src))
		if u == "" {
			continue
		}
		fields := strings.Fields(u)
		head := fields[0]
		switch head {
		case "read", "get", "select", "mget", "hget":
			return FRead
		case "write", "set", "put", "insert", "update", "upsert":
			return FWrite
		case "cas", "compare-and-set", "compare_and_set":
			return FCas
		case "add", "rpush", "lpush", "sadd":
			return FAdd
		case "enqueue", "publish", "produce":
			return FEnqueue
		case "dequeue", "consume", "poll", "lpop", "rpop":
			return FDequeue
		case "inc", "incr", "increment":
			return FInc
		case "generate", "next_id":
			return FGenerate
		case "txn", "transaction", "begin", "commit":
			return FTxn
		}
	}
	return name
}

func parseIntList(s string) []int64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return []int64{}
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var out []int64
	for _, p := range parts {
		if v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func parseOTelMicroOps(s string) []MicroOp {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		var ops []MicroOp
		if err := json.Unmarshal([]byte(s), &ops); err == nil {
			return ops
		}
	}
	return nil
}

func looksLikeOTel(s string) bool {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return false
	}
	return strings.Contains(t, `"resourceSpans"`) ||
		strings.Contains(t, `"scopeSpans"`) ||
		strings.Contains(t, `"instrumentationLibrarySpans"`) ||
		strings.Contains(t, `"startTimeUnixNano"`)
}
