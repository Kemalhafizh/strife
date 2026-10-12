package sim

// The client protocol shared by every simulated key-value system. Messages
// are plain values without pointers so that their %+v rendering, which feeds
// the trace hash, is deterministic.

// KVOp is a key-value operation.
type KVOp struct {
	F     string // read | write | cas
	Key   string
	Value int64 // write value, cas new value
	Old   int64 // cas expected value
}

// ClientRequest asks a server to execute Op.
type ClientRequest struct {
	ReqID uint64
	Op    KVOp
}

// ClientReply answers a ClientRequest.
type ClientReply struct {
	ReqID uint64
	// Status is "ok", "fail" (definitely not applied, e.g. cas mismatch) or
	// "redirect" (not applied: retry at Leader).
	Status string
	Value  int64
	Found  bool   // for reads: whether the key exists
	Leader NodeID // redirect hint, -1 if unknown
}

// Reply statuses.
const (
	StatusOK       = "ok"
	StatusFail     = "fail"
	StatusRedirect = "redirect"
)
