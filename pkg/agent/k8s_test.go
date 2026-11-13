package agent

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFIBRuleNetlinkMsgIPv4AndIPv6(t *testing.T) {
	pkt, err := BuildFIBRuleNetlinkMsg(RTMNewRule, 0x0505, 42, 1234, "10.244.1.15", false, StrifeFIBRulePriority)
	if err != nil {
		t.Fatalf("BuildFIBRuleNetlinkMsg IPv4: %v", err)
	}
	// 16 (nlmsghdr) + 12 (fib_rule_hdr) + 8 (FRA_DST 4B IPv4) + 8 (FRA_PRIORITY 4B) = 44 bytes
	if len(pkt) != 44 {
		t.Fatalf("expected 44-byte netlink packet for IPv4 rule, got %d", len(pkt))
	}
	msgLen := binary.LittleEndian.Uint32(pkt[0:4])
	msgType := binary.LittleEndian.Uint16(pkt[4:6])
	if msgLen != 44 || msgType != RTMNewRule {
		t.Fatalf("unexpected nlmsghdr len=%d type=%d", msgLen, msgType)
	}
	// Check fib_rule_hdr fields at offset 16..28
	family := pkt[16]
	dstLen := pkt[17]
	srcLen := pkt[18]
	action := pkt[23]
	if family != AfInet || dstLen != 32 || srcLen != 0 || action != FRActBlackhole {
		t.Fatalf("unexpected fib_rule_hdr: family=%d dstLen=%d srcLen=%d action=%d", family, dstLen, srcLen, action)
	}
	// First rtattr at offset 28: len=8, type=FRA_DST (1), payload=10.244.1.15
	rtaLen := binary.LittleEndian.Uint16(pkt[28:30])
	rtaType := binary.LittleEndian.Uint16(pkt[30:32])
	if rtaLen != 8 || rtaType != FRADst || pkt[32] != 10 || pkt[33] != 244 || pkt[34] != 1 || pkt[35] != 15 {
		t.Fatalf("unexpected FRA_DST attribute: len=%d type=%d ip=%v", rtaLen, rtaType, pkt[32:36])
	}
	// Second rtattr at offset 36: len=8, type=FRA_PRIORITY (6), payload=100
	prioLen := binary.LittleEndian.Uint16(pkt[36:38])
	prioType := binary.LittleEndian.Uint16(pkt[38:40])
	prioVal := binary.LittleEndian.Uint32(pkt[40:44])
	if prioLen != 8 || prioType != FRAPriority || prioVal != StrifeFIBRulePriority {
		t.Fatalf("unexpected FRA_PRIORITY attribute: len=%d type=%d val=%d", prioLen, prioType, prioVal)
	}

	// Verify IPv6 source rule
	pkt6, err := BuildFIBRuleNetlinkMsg(RTMDelRule, 0x0005, 43, 1234, "fd00::10/64", true, StrifeFIBRulePriority)
	if err != nil {
		t.Fatalf("BuildFIBRuleNetlinkMsg IPv6: %v", err)
	}
	// 16 + 12 + 20 (4 + 16B IPv6) + 8 = 56 bytes
	if len(pkt6) != 56 {
		t.Fatalf("expected 56-byte netlink packet for IPv6 rule, got %d", len(pkt6))
	}
	if pkt6[16] != AfInet6 || pkt6[18] != 64 || pkt6[23] != FRActBlackhole {
		t.Fatalf("unexpected IPv6 fib_rule_hdr: family=%d srcLen=%d action=%d", pkt6[16], pkt6[18], pkt6[23])
	}
}

func TestK8sResolverPodAndContainerCgroupDiscovery(t *testing.T) {
	tmp := t.TempDir()
	procRoot := filepath.Join(tmp, "proc")
	cgRoot := filepath.Join(tmp, "cgroup")

	// Create mock Kubernetes cgroup v2 hierarchy:
	// /sys/fs/cgroup/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod12345.slice/cri-containerd-a1b2c3d4e5f6.scope
	podRel := "kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod12345.slice"
	containerRel := podRel + "/cri-containerd-a1b2c3d4e5f6.scope"

	podCgDir := filepath.Join(cgRoot, filepath.FromSlash(podRel))
	containerCgDir := filepath.Join(cgRoot, filepath.FromSlash(containerRel))
	if err := os.MkdirAll(containerCgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create cgroup.freeze in pod slice and container scope
	if err := os.WriteFile(filepath.Join(podCgDir, "cgroup.freeze"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(containerCgDir, "cgroup.freeze"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(containerCgDir, "cgroup.procs"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create mock /proc/4242 for pod "payments/cockroachdb-0"
	pidDir := filepath.Join(procRoot, "4242")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cgroup"), []byte("0::/"+containerRel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte("/cockroach/cockroach\x00start\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	environ := "HOSTNAME=cockroachdb-0\x00POD_NAMESPACE=payments\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := &K8sResolver{
		ProcRoot:   procRoot,
		CgroupRoot: cgRoot,
	}

	// 1. Resolve pod:payments/cockroachdb-0 -> parent pod slice (which has cgroup.freeze)
	gotPodCg, err := resolver.ResolveCgroup("pod:payments/cockroachdb-0")
	if err != nil {
		t.Fatalf("ResolveCgroup pod:payments/cockroachdb-0: %v", err)
	}
	if gotPodCg != podCgDir {
		t.Fatalf("expected pod cgroup %q, got %q", podCgDir, gotPodCg)
	}

	// 2. Resolve container:a1b2c3d4e5f6 -> container scope directory
	gotContCg, err := resolver.ResolveCgroup("container:a1b2c3d4e5f6")
	if err != nil {
		t.Fatalf("ResolveCgroup container:a1b2c3d4e5f6: %v", err)
	}
	if gotContCg != containerCgDir {
		t.Fatalf("expected container cgroup %q, got %q", containerCgDir, gotContCg)
	}

	// 3. Resolve PIDs for pod:payments/cockroachdb-0
	pids, err := resolver.ResolvePIDs("pod:payments/cockroachdb-0")
	if err != nil || len(pids) != 1 || pids[0] != 4242 {
		t.Fatalf("expected PID [4242], got %v (err=%v)", pids, err)
	}

	// 4. Freeze via CgroupEngine using "pod:payments/cockroachdb-0"
	cgEngine := &CgroupEngine{K8s: resolver}
	if err := cgEngine.Freeze("pod:payments/cockroachdb-0"); err != nil {
		t.Fatalf("CgroupEngine.Freeze(pod:payments/cockroachdb-0): %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(podCgDir, "cgroup.freeze"))
	if string(b) != "1\n" {
		t.Fatalf("expected pod slice cgroup.freeze to be '1\\n', got %q", string(b))
	}
}

func TestParseK8sTargetWhitespace(t *testing.T) {
	if _, ok := ParseK8sTarget("pod:default/   "); ok {
		t.Fatal("expected ParseK8sTarget to reject whitespace-only pod name")
	}
	if _, ok := ParseK8sTarget("pod:   "); ok {
		t.Fatal("expected ParseK8sTarget to reject empty pod spec")
	}
	got, ok := ParseK8sTarget("  pod: payments / db-0 ")
	if !ok || got.Namespace != "payments" || got.PodName != "db-0" {
		t.Fatalf("unexpected parsed target: %+v ok=%v", got, ok)
	}
}
