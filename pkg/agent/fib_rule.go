package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
)

// Linux RTNETLINK FIB policy routing rule constants (include/uapi/linux/fib_rules.h
// and include/uapi/linux/rtnetlink.h).
const (
	RTMNewRule = 32 // RTM_NEWRULE
	RTMDelRule = 33 // RTM_DELRULE

	FRActBlackhole = 6   // FR_ACT_BLACKHOLE
	RTTableMain    = 254 // RT_TABLE_MAIN
	RTTableUnspec  = 0   // RT_TABLE_UNSPEC

	FRADst      = 1 // FRA_DST (destination prefix)
	FRASrc      = 2 // FRA_SRC (source prefix)
	FRAPriority = 6 // FRA_PRIORITY (rule preference/priority)

	AfInet  = 2  // AF_INET
	AfInet6 = 10 // AF_INET6

	NlmsgHdrLen = 16 // sizeof(struct nlmsghdr)

	// StrifeFIBRulePriority is the high-precedence rule priority used by
	// strife-agent so partition blackhole rules evaluate before RT_TABLE_MAIN (32766).
	StrifeFIBRulePriority uint32 = 100
)

// fibRuleHdr mirrors struct fib_rule_hdr from include/uapi/linux/fib_rules.h (12 bytes).
type fibRuleHdr struct {
	Family uint8
	DstLen uint8
	SrcLen uint8
	Tos    uint8
	Table  uint8
	Res1   uint8
	Res2   uint8
	Action uint8
	Flags  uint32
}

// ParseTargetIP extracts the address family (AF_INET or AF_INET6), prefix bit
// length, and raw address bytes for a host IP or CIDR string.
func ParseTargetIP(ipStr string) (family uint8, bitLen uint8, rawIP []byte, err error) {
	if ip, ipNet, cidrErr := net.ParseCIDR(ipStr); cidrErr == nil {
		ones, _ := ipNet.Mask.Size()
		if ip4 := ip.To4(); ip4 != nil {
			return AfInet, uint8(ones), ip4, nil
		}
		if ip16 := ip.To16(); ip16 != nil {
			return AfInet6, uint8(ones), ip16, nil
		}
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return 0, 0, nil, fmt.Errorf("netlink: invalid target IP %q", ipStr)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return AfInet, 32, ip4, nil
	}
	if ip16 := ip.To16(); ip16 != nil {
		return AfInet6, 128, ip16, nil
	}
	return 0, 0, nil, fmt.Errorf("netlink: unsupported IP address %q", ipStr)
}

// BuildFIBRulePayload constructs the RTNETLINK fib_rule_hdr and aligned rtattr
// attributes (FRA_DST or FRA_SRC plus FRA_PRIORITY) for a kernel FIB blackhole rule.
func BuildFIBRulePayload(ipStr string, isSrc bool, priority uint32) (uint8, []byte, error) {
	family, bitLen, rawIP, err := ParseTargetIP(ipStr)
	if err != nil {
		return 0, nil, err
	}

	hdr := fibRuleHdr{
		Family: family,
		Table:  RTTableUnspec,
		Action: FRActBlackhole,
	}
	attrType := uint16(FRADst)
	if isSrc {
		hdr.SrcLen = bitLen
		attrType = FRASrc
	} else {
		hdr.DstLen = bitLen
	}

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, hdr); err != nil {
		return 0, nil, err
	}
	writeRTAttr(&buf, attrType, rawIP)
	var prioBytes [4]byte
	binary.LittleEndian.PutUint32(prioBytes[:], priority)
	writeRTAttr(&buf, FRAPriority, prioBytes[:])

	return family, buf.Bytes(), nil
}

// BuildFIBRuleNetlinkMsg constructs a complete 16-byte nlmsghdr + fib_rule_hdr +
// rtattr packet ready to send over an AF_NETLINK (NETLINK_ROUTE) socket.
func BuildFIBRuleNetlinkMsg(msgType, flags uint16, seq, pid uint32, ipStr string, isSrc bool, priority uint32) ([]byte, error) {
	_, payload, err := BuildFIBRulePayload(ipStr, isSrc, priority)
	if err != nil {
		return nil, err
	}
	totalLen := uint32(NlmsgHdrLen + len(payload))
	var msg bytes.Buffer
	msg.Grow(int(totalLen))
	_ = binary.Write(&msg, binary.LittleEndian, totalLen)
	_ = binary.Write(&msg, binary.LittleEndian, msgType)
	_ = binary.Write(&msg, binary.LittleEndian, flags)
	_ = binary.Write(&msg, binary.LittleEndian, seq)
	_ = binary.Write(&msg, binary.LittleEndian, pid)
	msg.Write(payload)
	return msg.Bytes(), nil
}

func writeRTAttr(buf *bytes.Buffer, attrType uint16, data []byte) {
	attrLen := uint16(4 + len(data))
	pad := (4 - (len(data) % 4)) % 4
	_ = binary.Write(buf, binary.LittleEndian, attrLen)
	_ = binary.Write(buf, binary.LittleEndian, attrType)
	buf.Write(data)
	if pad > 0 {
		buf.Write(make([]byte, pad))
	}
}
