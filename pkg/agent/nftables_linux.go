//go:build linux

package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// The agent's partition backend talks nf_tables over netlink directly: one
// atomic batch per change instead of an iptables process per rule.
//
// It owns two tables, "ip strife" and "ip6 strife", each with one base chain
// hooked into input at filter priority and one rule per blocked source:
//
//	table ip strife {
//		chain input {
//			type filter hook input priority filter; policy accept;
//			ip saddr 10.0.0.2 drop
//		}
//	}
//
// Every change rewrites the whole chain inside a batch (flush, then add), so
// the kernel never holds a partial rule set and the agent never needs rule
// handles. Deleting the tables removes everything the agent ever added.

// Netfilter netlink constants (include/uapi/linux/netfilter/nfnetlink.h,
// nf_tables.h). Integer attributes are big-endian.
const (
	nfnlSubsysNFTables = 10
	nfnlMsgBatchBegin  = 0x10
	nfnlMsgBatchEnd    = 0x11

	nftMsgNewTable = 0
	nftMsgDelTable = 2
	nftMsgNewChain = 3
	nftMsgNewRule  = 6
	nftMsgDelRule  = 8

	nfprotoIPv4 = 2
	nfprotoIPv6 = 10

	nlaFNested = 0x8000
	nlmFExcl   = 0x200

	nftaTableName = 1

	nftaChainTable = 1
	nftaChainName  = 3
	nftaChainHook  = 4
	nftaChainType  = 7
	nftaHookNum    = 1
	nftaHookPrio   = 2

	nftaRuleTable = 1
	nftaRuleChain = 2
	nftaRuleExprs = 4

	nftaListElem = 1
	nftaExprName = 1
	nftaExprData = 2

	nftaPayloadDreg   = 1
	nftaPayloadBase   = 2
	nftaPayloadOffset = 3
	nftaPayloadLen    = 4
	nftPayloadNetwork = 1

	nftaCmpSreg = 1
	nftaCmpOp   = 2
	nftaCmpData = 3
	nftCmpEq    = 0

	nftaImmDreg     = 1
	nftaImmData     = 2
	nftaDataValue   = 1
	nftaDataVerdict = 2
	nftaVerdictCode = 1

	nftRegVerdict = 0
	nftReg1       = 1
	nfDrop        = 0
	nfAccept      = 1
	nfInetLocalIn = 1
)

// nftTable and nftChain are the names the agent owns in both families.
const (
	nftTable = "strife"
	nftChain = "input"
)

var nftSeq atomic.Uint32

// nftablesFilter implements filterBackend with nf_tables netlink.
//
// Adding a rule costs the kernel tens of microseconds, but removing one (or
// redefining a base chain) makes the commit wait for an RCU grace period,
// which is milliseconds. So the tables and chains are created once, and a
// change never removes anything on the hot path: it inserts, at the head of
// the chain, "drop" for each newly blocked source and "accept" for each
// newly unblocked one. The first matching rule decides, so the newest
// decision wins. Compact later rewrites the chain to its minimal form (one
// drop rule per blocked source), off the fault's critical path.
type nftablesFilter struct {
	ready   bool
	blocked map[uint8]map[string]bool // effective state, per family
	rules   map[uint8]int             // rules in each chain
}

// maxRules triggers an immediate compaction: rules are matched in order,
// so the chain must not grow without bound between idle periods.
const maxRules = 128

func newNFTablesBackend() filterBackend { return &nftablesFilter{} }

func (*nftablesFilter) Name() string { return "nftables" }

func familyOf(ip net.IP) uint8 {
	if ip.To4() != nil {
		return nfprotoIPv4
	}
	return nfprotoIPv6
}

// Available creates the agent's tables and chains and empties them, so the
// filter starts from a known state even if a previous agent left rules.
func (n *nftablesFilter) Available() bool {
	var b nftBatch
	b.begin()
	for _, fam := range []uint8{nfprotoIPv4, nfprotoIPv6} {
		b.add(nftMsgNewTable, fam, nlmFCreate, nftTableAttrs())
		b.add(nftMsgNewChain, fam, nlmFCreate, nftChainAttrs())
		b.add(nftMsgDelRule, fam, 0, nftRuleKey())
	}
	b.end()
	if b.send(false) != nil {
		return false
	}
	n.ready = true
	n.blocked = map[uint8]map[string]bool{nfprotoIPv4: {}, nfprotoIPv6: {}}
	n.rules = map[uint8]int{}
	return true
}

// Sync makes the input chains drop exactly the given sources, in one
// atomic batch of insertions.
func (n *nftablesFilter) Sync(ips []net.IP) error {
	if !n.ready && !n.Available() {
		return errors.New("nftables: cannot create the strife tables")
	}
	want := map[uint8]map[string]net.IP{nfprotoIPv4: {}, nfprotoIPv6: {}}
	for _, ip := range ips {
		want[familyOf(ip)][ip.String()] = ip
	}
	var b nftBatch
	b.begin()
	added := map[uint8]int{}
	for _, fam := range []uint8{nfprotoIPv4, nfprotoIPv6} {
		for k, ip := range want[fam] {
			if !n.blocked[fam][k] {
				b.add(nftMsgNewRule, fam, nlmFCreate, nftVerdictRule(ip, nfDrop))
				added[fam]++
			}
		}
		for k := range n.blocked[fam] {
			if _, ok := want[fam][k]; !ok {
				b.add(nftMsgNewRule, fam, nlmFCreate, nftVerdictRule(net.ParseIP(k), nfAccept))
				added[fam]++
			}
		}
	}
	b.end()
	if len(b.acks) == 0 {
		return nil // already in effect
	}
	if err := b.send(false); err != nil {
		return err // the batch was aborted: nothing changed
	}
	for _, fam := range []uint8{nfprotoIPv4, nfprotoIPv6} {
		n.blocked[fam] = map[string]bool{}
		for k := range want[fam] {
			n.blocked[fam][k] = true
		}
		n.rules[fam] += added[fam]
	}
	if n.rules[nfprotoIPv4] > maxRules || n.rules[nfprotoIPv6] > maxRules {
		return n.Compact()
	}
	return nil
}

// Compact rewrites each chain that holds more rules than blocked sources to
// one drop rule per blocked source. It pays the kernel's grace period, so
// the agent runs it when no fault is changing.
func (n *nftablesFilter) Compact() error {
	if !n.ready {
		return nil
	}
	var b nftBatch
	b.begin()
	for _, fam := range []uint8{nfprotoIPv4, nfprotoIPv6} {
		if n.rules[fam] == len(n.blocked[fam]) {
			continue
		}
		b.add(nftMsgDelRule, fam, 0, nftRuleKey())
		for k := range n.blocked[fam] {
			b.add(nftMsgNewRule, fam, nlmFCreate|0x800 /* NLM_F_APPEND */, nftVerdictRule(net.ParseIP(k), nfDrop))
		}
	}
	b.end()
	if len(b.acks) == 0 {
		return nil
	}
	if err := b.send(false); err != nil {
		return err
	}
	for _, fam := range []uint8{nfprotoIPv4, nfprotoIPv6} {
		n.rules[fam] = len(n.blocked[fam])
	}
	return nil
}

// Teardown deletes the agent's tables, and with them every rule. Each
// family is its own batch: a missing table aborts the batch it is in.
func (n *nftablesFilter) Teardown() error {
	for _, fam := range []uint8{nfprotoIPv4, nfprotoIPv6} {
		var b nftBatch
		b.begin()
		b.add(nftMsgDelTable, fam, 0, nftTableAttrs())
		b.end()
		if err := b.send(true); err != nil {
			return err
		}
	}
	n.ready = false
	return nil
}

func nftTableAttrs() []byte {
	var a bytes.Buffer
	writeAttr(&a, nftaTableName, cstr(nftTable))
	return a.Bytes()
}

func nftChainAttrs() []byte {
	var hook bytes.Buffer
	writeAttr(&hook, nftaHookNum, be32(nfInetLocalIn))
	writeAttr(&hook, nftaHookPrio, be32(0)) // NF_IP_PRI_FILTER
	var a bytes.Buffer
	writeAttr(&a, nftaChainTable, cstr(nftTable))
	writeAttr(&a, nftaChainName, cstr(nftChain))
	writeAttr(&a, nftaChainHook|nlaFNested, hook.Bytes())
	writeAttr(&a, nftaChainType, cstr("filter"))
	return a.Bytes()
}

func nftRuleKey() []byte {
	var a bytes.Buffer
	writeAttr(&a, nftaRuleTable, cstr(nftTable))
	writeAttr(&a, nftaRuleChain, cstr(nftChain))
	return a.Bytes()
}

// nftVerdictRule encodes "ip saddr X drop" (or accept, or the ip6 forms):
// load the source address from the network header into register 1,
// compare, and set the verdict register.
func nftVerdictRule(ip net.IP, code uint32) []byte {
	offset, addr := uint32(12), []byte(ip.To4())
	if addr == nil {
		offset, addr = 8, []byte(ip.To16())
	}
	var payload bytes.Buffer
	writeAttr(&payload, nftaPayloadDreg, be32(nftReg1))
	writeAttr(&payload, nftaPayloadBase, be32(nftPayloadNetwork))
	writeAttr(&payload, nftaPayloadOffset, be32(offset))
	writeAttr(&payload, nftaPayloadLen, be32(uint32(len(addr))))

	var value bytes.Buffer
	writeAttr(&value, nftaDataValue, addr)
	var cmp bytes.Buffer
	writeAttr(&cmp, nftaCmpSreg, be32(nftReg1))
	writeAttr(&cmp, nftaCmpOp, be32(nftCmpEq))
	writeAttr(&cmp, nftaCmpData|nlaFNested, value.Bytes())

	var vcode bytes.Buffer
	writeAttr(&vcode, nftaVerdictCode, be32(code))
	var verdict bytes.Buffer
	writeAttr(&verdict, nftaDataVerdict|nlaFNested, vcode.Bytes())
	var imm bytes.Buffer
	writeAttr(&imm, nftaImmDreg, be32(nftRegVerdict))
	writeAttr(&imm, nftaImmData|nlaFNested, verdict.Bytes())

	var exprs bytes.Buffer
	for _, e := range []struct {
		name string
		data []byte
	}{{"payload", payload.Bytes()}, {"cmp", cmp.Bytes()}, {"immediate", imm.Bytes()}} {
		var elem bytes.Buffer
		writeAttr(&elem, nftaExprName, cstr(e.name))
		writeAttr(&elem, nftaExprData|nlaFNested, e.data)
		writeAttr(&exprs, nftaListElem|nlaFNested, elem.Bytes())
	}

	a := bytes.NewBuffer(nftRuleKey())
	writeAttr(a, nftaRuleExprs|nlaFNested, exprs.Bytes())
	return a.Bytes()
}

func cstr(s string) []byte { return append([]byte(s), 0) }

func be32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

// nftBatch accumulates netlink messages framed by batch begin/end.
type nftBatch struct {
	buf  bytes.Buffer
	acks []uint32 // sequence numbers that will be acknowledged
}

func (b *nftBatch) msg(typ uint16, flags uint16, family uint8, resID uint16, attrs []byte) uint32 {
	seq := nftSeq.Add(1)
	hdr := syscall.NlMsghdr{
		Len:   uint32(syscall.SizeofNlMsghdr + 4 + len(attrs)),
		Type:  typ,
		Flags: syscall.NLM_F_REQUEST | flags,
		Seq:   seq,
	}
	var raw [syscall.SizeofNlMsghdr]byte
	*(*syscall.NlMsghdr)(unsafe.Pointer(&raw[0])) = hdr
	b.buf.Write(raw[:])
	b.buf.Write([]byte{family, 0}) // nfgenmsg: family, NFNETLINK_V0
	var id [2]byte
	binary.BigEndian.PutUint16(id[:], resID)
	b.buf.Write(id[:])
	b.buf.Write(attrs) // attributes are already 4-byte aligned
	return seq
}

func (b *nftBatch) begin() { b.msg(nfnlMsgBatchBegin, 0, syscall.AF_UNSPEC, nfnlSubsysNFTables, nil) }
func (b *nftBatch) end()   { b.msg(nfnlMsgBatchEnd, 0, syscall.AF_UNSPEC, nfnlSubsysNFTables, nil) }

func (b *nftBatch) add(msg uint16, family uint8, flags uint16, attrs []byte) {
	seq := b.msg(nfnlSubsysNFTables<<8|msg, syscall.NLM_F_ACK|flags, family, 0, attrs)
	b.acks = append(b.acks, seq)
}

// send submits the batch and waits for an ack of every message. With
// missingOK, ENOENT (deleting what is not there) counts as success.
func (b *nftBatch) send(missingOK bool) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_NETFILTER)
	if err != nil {
		return fmt.Errorf("nftables: netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &netlinkTimeout); err != nil {
		return err
	}
	if err := syscall.Sendto(fd, b.buf.Bytes(), 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return fmt.Errorf("nftables: send: %w", err)
	}
	pending := map[uint32]bool{}
	for _, s := range b.acks {
		pending[s] = true
	}
	rb := make([]byte, 1<<16)
	for len(pending) > 0 {
		n, _, err := syscall.Recvfrom(fd, rb, 0)
		if err != nil {
			return fmt.Errorf("nftables: waiting for %d acks: %w", len(pending), err)
		}
		msgs, err := syscall.ParseNetlinkMessage(rb[:n])
		if err != nil {
			return fmt.Errorf("nftables: %w", err)
		}
		for _, m := range msgs {
			if m.Header.Type != syscall.NLMSG_ERROR || !pending[m.Header.Seq] {
				continue
			}
			if len(m.Data) < 4 {
				return errors.New("nftables: truncated ack")
			}
			errno := syscall.Errno(-int32(binary.LittleEndian.Uint32(m.Data[:4])))
			if errno != 0 && !(missingOK && errno == syscall.ENOENT) {
				// The kernel aborted the whole batch: nothing was applied.
				return fmt.Errorf("nftables: %w", errno)
			}
			delete(pending, m.Header.Seq)
		}
	}
	return nil
}
