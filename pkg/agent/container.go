package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// container is a container this agent injects faults into from outside: an
// agent deployed once per node serves every pod on it, so each request
// names its target, and the agent acts in that container's network
// namespace, on its processes only, and on its cgroup.
type container struct {
	id     string
	pid    int    // its init process, as the agent sees it
	cgroup string // its cgroup v2 directory, "" when none was found
}

// ErrUnknownContainer means no process of the container runs on this node:
// it has been replaced (a new ID) or runs elsewhere. Clients re-resolve the
// container's ID when they see it.
var ErrUnknownContainer = errors.New("agent: unknown container")

var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// normalizeContainerID strips a runtime prefix ("containerd://") and checks
// that what remains is a container ID, so it can be matched against cgroup
// paths without matching anything else.
func normalizeContainerID(s string) (string, error) {
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if !containerIDPattern.MatchString(s) {
		return "", fmt.Errorf("agent: %q is not a container ID", s)
	}
	return s, nil
}

// containerIndex finds containers by ID through /proc and the cgroup
// filesystem, and remembers them while their init process lives.
type containerIndex struct {
	proc       string // procfs mount, "/proc"
	cgroupRoot string // the node's cgroup v2 mount as the agent sees it

	mu   sync.Mutex
	byID map[string]*container
}

func newContainerIndex(proc, cgroupRoot string) *containerIndex {
	return &containerIndex{proc: proc, cgroupRoot: cgroupRoot, byID: map[string]*container{}}
}

// resolve returns the container with this ID.
func (x *containerIndex) resolve(rawID string) (*container, error) {
	id, err := normalizeContainerID(rawID)
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if c, ok := x.byID[id]; ok && x.belongs(c.pid, id) {
		return c, nil
	}
	delete(x.byID, id)
	pid, err := x.initOf(id)
	if err != nil {
		return nil, err
	}
	c := &container{id: id, pid: pid, cgroup: x.cgroupDir(pid, id)}
	x.byID[id] = c
	return c, nil
}

// belongs reports whether pid runs in container id: its cgroup path names
// the container, which is how every runtime lays out container cgroups.
func (x *containerIndex) belongs(pid int, id string) bool {
	data, err := os.ReadFile(filepath.Join(x.proc, strconv.Itoa(pid), "cgroup"))
	return err == nil && bytes.Contains(data, []byte(id))
}

// initOf finds the container's init: the process that is PID 1 in its own
// PID namespace, else the oldest one (a container sharing the node's PID
// namespace).
func (x *containerIndex) initOf(id string) (int, error) {
	entries, err := os.ReadDir(x.proc)
	if err != nil {
		return 0, err
	}
	oldest := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !x.belongs(pid, id) {
			continue
		}
		if x.nsInit(pid) {
			return pid, nil
		}
		if oldest == 0 || pid < oldest {
			oldest = pid
		}
	}
	if oldest == 0 {
		return 0, fmt.Errorf("%w %.12s: none of its processes runs on this node (the agent must share the node's PID namespace)", ErrUnknownContainer, id)
	}
	return oldest, nil
}

// nsInit reports whether pid is PID 1 of a nested PID namespace.
func (x *containerIndex) nsInit(pid int) bool {
	f, err := os.Open(filepath.Join(x.proc, strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "NSpid:"); ok {
			ids := strings.Fields(v)
			return len(ids) > 1 && ids[len(ids)-1] == "1"
		}
	}
	return false
}

// cgroupDir finds the container's cgroup v2 directory under the cgroup root:
// the path /proc/<pid>/cgroup names when the agent shares the node's cgroup
// namespace, else the directory named after the container (a Kubernetes
// pod has a cgroup namespace of its own, so the path it reads is relative).
func (x *containerIndex) cgroupDir(pid int, id string) string {
	if x.cgroupRoot == "" {
		return ""
	}
	if data, err := os.ReadFile(filepath.Join(x.proc, strconv.Itoa(pid), "cgroup")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			p, ok := strings.CutPrefix(line, "0::")
			if !ok || strings.Contains(p, "..") || !strings.Contains(p, id) {
				continue
			}
			if d := filepath.Join(x.cgroupRoot, p); isDir(d) {
				return d
			}
		}
	}
	found := ""
	_ = filepath.WalkDir(x.cgroupRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if strings.Count(strings.TrimPrefix(path, x.cgroupRoot), "/") > 10 {
			return fs.SkipDir
		}
		if strings.Contains(d.Name(), id) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// defaultCgroupRoot is the node's cgroup v2 mount: /sys/fs/cgroup, or its
// unified half on a hybrid cgroup v1/v2 host.
func defaultCgroupRoot() string {
	for _, p := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/unified"} {
		if _, err := os.Stat(filepath.Join(p, "cgroup.controllers")); err == nil {
			return p
		}
	}
	return ""
}

// environOf returns the environment pid started with, so commands run on
// its behalf (restarting the database inside its container) see the same
// variables.
func environOf(proc string, pid int) []string {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil
	}
	var env []string
	for _, kv := range bytes.Split(data, []byte{0}) {
		if len(kv) > 0 && bytes.IndexByte(kv, '=') > 0 {
			env = append(env, string(kv))
		}
	}
	return env
}
