package agent

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// K8sTarget represents a parsed Kubernetes Pod or Container target specifier.
type K8sTarget struct {
	Kind        string // "pod" or "container"
	Namespace   string
	PodName     string
	ContainerID string
}

// ParseK8sTarget parses a target specifier such as:
//   - "pod:default/etcd-0"
//   - "pod:etcd-0"
//   - "k8s:default/etcd-0"
//   - "k8s:etcd-0"
//   - "container:a1b2c3d4e5f6"
//
// It returns ok=false when spec is a plain process pattern or filesystem path.
func ParseK8sTarget(spec string) (K8sTarget, bool) {
	spec = strings.TrimSpace(spec)
	for _, prefix := range []string{"pod:", "k8s:"} {
		if rest, ok := strings.CutPrefix(spec, prefix); ok {
			rest = strings.TrimSpace(rest)
			if rest == "" {
				return K8sTarget{}, false
			}
			if ns, name, hasSlash := strings.Cut(rest, "/"); hasSlash {
				ns = strings.TrimSpace(ns)
				name = strings.TrimSpace(name)
				if name == "" {
					return K8sTarget{}, false
				}
				return K8sTarget{
					Kind:      "pod",
					Namespace: ns,
					PodName:   name,
				}, true
			}
			return K8sTarget{
				Kind:    "pod",
				PodName: rest,
			}, true
		}
	}
	if cid, ok := strings.CutPrefix(spec, "container:"); ok {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			return K8sTarget{}, false
		}
		return K8sTarget{
			Kind:        "container",
			ContainerID: cid,
		}, true
	}
	return K8sTarget{}, false
}

// K8sResolver discovers Kubernetes Pod and Container cgroup v2 directories,
// host PIDs, and network namespace paths directly from /proc and /sys/fs/cgroup
// without requiring kubectl, crictl, or container runtime sockets.
type K8sResolver struct {
	ProcRoot   string
	CgroupRoot string
}

// NewK8sResolver creates a K8sResolver rooted at /proc and /sys/fs/cgroup.
func NewK8sResolver() *K8sResolver {
	return &K8sResolver{
		ProcRoot:   "/proc",
		CgroupRoot: "/sys/fs/cgroup",
	}
}

// Available reports whether /proc and /sys/fs/cgroup are present on the host.
func (r *K8sResolver) Available() bool {
	if _, err := os.Stat(r.ProcRoot); err != nil {
		return false
	}
	if _, err := os.Stat(r.CgroupRoot); err != nil {
		return false
	}
	return true
}

// ResolveCgroup resolves a "pod:...", "k8s:...", or "container:..." specifier
// to its absolute cgroup v2 directory path under CgroupRoot. Plain paths are
// returned unchanged.
func (r *K8sResolver) ResolveCgroup(spec string) (string, error) {
	target, ok := ParseK8sTarget(spec)
	if !ok {
		return spec, nil
	}

	// 1. For container:<id>, first search CgroupRoot for a matching container
	// scope directory (cri-containerd-<id>.scope, docker-<id>.scope, crio-<id>.scope, or <id>).
	if target.Kind == "container" {
		if dir, err := r.findContainerCgroupDir(target.ContainerID); err == nil && dir != "" {
			return dir, nil
		}
	}

	// 2. Scan /proc/<pid> for processes matching the pod or container and
	// resolve their /proc/<pid>/cgroup relative hierarchy path.
	matches := r.scanMatchingProcs(target)
	for _, m := range matches {
		if m.cgroupRel != "" {
			rel := strings.TrimPrefix(m.cgroupRel, "/")
			full := filepath.Join(r.CgroupRoot, filepath.FromSlash(rel))
			if target.Kind == "pod" {
				// If the process is inside a container scope under a pod slice
				// (e.g., .../kubepods-pod<uid>.slice/cri-containerd-<cid>.scope),
				// freezing the parent pod slice freezes the entire Pod atomically
				// if the parent slice has cgroup.freeze; otherwise use the container scope.
				parent := filepath.Dir(full)
				if strings.Contains(filepath.Base(parent), "pod") {
					if _, err := os.Stat(filepath.Join(parent, "cgroup.freeze")); err == nil {
						return parent, nil
					}
				}
			}
			return full, nil
		}
	}

	return "", fmt.Errorf("k8s: could not resolve cgroup v2 path for %q", spec)
}

// ResolvePIDs discovers all host PIDs belonging to a "pod:...", "k8s:...", or
// "container:..." specifier.
func (r *K8sResolver) ResolvePIDs(spec string) ([]int, error) {
	target, ok := ParseK8sTarget(spec)
	if !ok {
		return nil, fmt.Errorf("k8s: %q is not a pod or container specifier", spec)
	}

	pidSet := make(map[int]bool)
	myPID := os.Getpid()

	// 1. Collect PIDs from /proc scan.
	matches := r.scanMatchingProcs(target)
	for _, m := range matches {
		if m.pid != myPID && !m.isPause {
			pidSet[m.pid] = true
		}
	}

	// 2. Also read cgroup.procs from the resolved cgroup directory (if present).
	if cgDir, err := r.ResolveCgroup(spec); err == nil && cgDir != "" {
		for _, pid := range readCgroupProcsTree(cgDir) {
			if pid != myPID {
				pidSet[pid] = true
			}
		}
	}

	if len(pidSet) == 0 {
		return nil, fmt.Errorf("k8s: no host processes found for %q", spec)
	}
	pids := make([]int, 0, len(pidSet))
	for pid := range pidSet {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

// ResolveNetnsPath returns the /proc/<pid>/ns/net path for the first process in
// the target Kubernetes Pod or Container.
func (r *K8sResolver) ResolveNetnsPath(spec string) (string, error) {
	pids, err := r.ResolvePIDs(spec)
	if err != nil || len(pids) == 0 {
		return "", fmt.Errorf("k8s: cannot resolve network namespace for %q: %w", spec, err)
	}
	return filepath.Join(r.ProcRoot, strconv.Itoa(pids[0]), "ns", "net"), nil
}

type procMatch struct {
	pid       int
	cgroupRel string
	isPause   bool
}

func (r *K8sResolver) scanMatchingProcs(target K8sTarget) []procMatch {
	entries, err := os.ReadDir(r.ProcRoot)
	if err != nil {
		return nil
	}

	var out []procMatch
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		procDir := filepath.Join(r.ProcRoot, entry.Name())
		cgRel := readProcCgroupV2(filepath.Join(procDir, "cgroup"))
		cmdline := readProcCmdline(filepath.Join(procDir, "cmdline"))
		if strings.Contains(cmdline, "strife-agent") {
			continue
		}
		isPause := strings.HasPrefix(cmdline, "/pause") || cmdline == "pause"

		switch target.Kind {
		case "container":
			if containsContainerID(cgRel, target.ContainerID) || r.procMatchesContainerHostname(procDir, target.ContainerID) {
				out = append(out, procMatch{pid: pid, cgroupRel: cgRel, isPause: isPause})
			}
		case "pod":
			if r.procMatchesPod(procDir, cgRel, target.Namespace, target.PodName) {
				out = append(out, procMatch{pid: pid, cgroupRel: cgRel, isPause: isPause})
			}
		}
	}
	// Prefer non-pause application containers first so cgroup resolution picks
	// an active workload container when needed.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].isPause != out[j].isPause {
			return !out[i].isPause
		}
		return out[i].pid < out[j].pid
	})
	return out
}

func (r *K8sResolver) procMatchesContainerHostname(procDir, cid string) bool {
	env := readProcEnviron(filepath.Join(procDir, "environ"))
	h := env["HOSTNAME"]
	return h != "" && (strings.HasPrefix(cid, h) || strings.HasPrefix(h, cid))
}

func (r *K8sResolver) procMatchesPod(procDir, cgRel, wantNS, wantPod string) bool {
	env := readProcEnviron(filepath.Join(procDir, "environ"))

	podName := firstNonEmpty(env["POD_NAME"], env["K8S_POD_NAME"], env["HOSTNAME"])
	if podName == "" {
		if b, err := os.ReadFile(filepath.Join(procDir, "root", "etc", "hostname")); err == nil {
			podName = strings.TrimSpace(string(b))
		}
	}
	if podName != wantPod && !strings.Contains(cgRel, wantPod) {
		return false
	}

	if wantNS == "" {
		return true
	}
	ns := firstNonEmpty(env["POD_NAMESPACE"], env["K8S_POD_NAMESPACE"], env["KUBERNETES_NAMESPACE"])
	if ns == "" {
		if b, err := os.ReadFile(filepath.Join(procDir, "root", "var", "run", "secrets", "kubernetes.io", "serviceaccount", "namespace")); err == nil {
			ns = strings.TrimSpace(string(b))
		}
	}
	return ns == wantNS
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (r *K8sResolver) findContainerCgroupDir(cid string) (string, error) {
	var found string
	_ = filepath.WalkDir(r.CgroupRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if containsContainerID(d.Name(), cid) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("container cgroup %q not found", cid)
}

func containsContainerID(s, cid string) bool {
	return s != "" && cid != "" && strings.Contains(s, cid)
}

func readProcCgroupV2(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var fallback string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.TrimSpace(after)
		}
		if parts := strings.SplitN(line, ":", 3); len(parts) == 3 && parts[2] != "" {
			fallback = parts[2]
		}
	}
	return fallback
}

func readProcCmdline(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.ReplaceAll(data, []byte{0}, []byte{' '})))
}

func readProcEnviron(path string) map[string]string {
	out := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, entry := range bytes.Split(data, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if k, v, ok := strings.Cut(string(entry), "="); ok {
			out[k] = v
		}
	}
	return out
}

func readCgroupProcsTree(root string) []int {
	var pids []int
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "cgroup.procs" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 1 {
				pids = append(pids, pid)
			}
		}
		return nil
	})
	return pids
}
