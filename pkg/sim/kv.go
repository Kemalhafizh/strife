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

// Ledger operations carried in KVOp.F when Raft is configured as a
// double-entry financial ledger.
const (
	FLedgerTransfer = "ledger-transfer"
	FLedgerAudit    = "ledger-audit"
)

// LedgerReply answers a ClientRequest for a double-entry financial ledger
// operation (FLedgerTransfer or FLedgerAudit).
type LedgerReply struct {
	ReqID    uint64
	Status   string
	Leader   NodeID
	OldFrom  int64
	OldTo    int64
	NewFrom  int64
	NewTo    int64
	Balances []int64
}

// Settlement operations carried in KVOp.F when Raft is configured as a
// cross-system transactional outbox / settlement pipeline.
const (
	FSettlementWrite = "settlement-write"
	FSettlementAudit = "settlement-audit"
)

// SettlementReply answers a ClientRequest for a cross-system settlement
// operation (FSettlementWrite or FSettlementAudit).
type SettlementReply struct {
	ReqID  uint64
	Status string
	Leader NodeID
	TxID   int64
	Ledger []int64
	Broker []int64
}
