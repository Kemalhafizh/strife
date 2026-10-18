package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeContainerID(t *testing.T) {
	id := strings.Repeat("0a", 32)
	for in, want := range map[string]string{
		"containerd://" + id: id,
		"docker://" + id:     id,
		"cri-o://" + id:      id,
		strings.ToUpper(id):  id,
		id[:12]:              id[:12],
	} {
		if got, err := normalizeContainerID(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "../../etc", id + "/..", "containerd://" + id[:11]} {
		if _, err := normalizeContainerID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakeNode lays out /proc and a cgroup v2 tree the way a node with a
// container looks, without needing one.
func fakeNode(t *testing.T, id, cgroupLine string) (proc, cgroot string) {
	t.Helper()
	base := t.TempDir()
	proc, cgroot = filepath.Join(base, "proc"), filepath.Join(base, "cgroup")
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// pid 10: an unrelated host process; 41 and 40: the container's
	// shell and its init (PID 1 in its namespace).
	write(filepath.Join(proc, "10", "cgroup"), "0::/system.slice/sshd.service\n")
	write(filepath.Join(proc, "10", "status"), "Name:\tsshd\nNSpid:\t10\n")
	write(filepath.Join(proc, "41", "cgroup"), cgroupLine)
	write(filepath.Join(proc, "41", "status"), "NSpid:\t41\t7\n")
	write(filepath.Join(proc, "40", "cgroup"), cgroupLine)
	write(filepath.Join(proc, "40", "status"), "NSpid:\t40\t1\n")
	if err := os.MkdirAll(filepath.Join(cgroot, "kubepods.slice", "kubepods-pod1.slice", "cri-containerd-"+id+".scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	return proc, cgroot
}

func TestContainerIndexResolves(t *testing.T) {
	id := strings.Repeat("be", 32)
	want := "kubepods.slice/kubepods-pod1.slice/cri-containerd-" + id + ".scope"

	// The agent shares the node's cgroup namespace: the path is absolute.
	proc, cgroot := fakeNode(t, id, "0::/"+want+"\n")
	c, err := newContainerIndex(proc, cgroot).resolve("containerd://" + id)
	if err != nil {
		t.Fatal(err)
	}
	if c.pid != 40 || c.cgroup != filepath.Join(cgroot, want) {
		t.Fatalf("got pid %d cgroup %s", c.pid, c.cgroup)
	}

	// A pod has a cgroup namespace of its own: the path the agent reads is
	// relative, and the directory is found by the container's ID.
	proc, cgroot = fakeNode(t, id, "0::/../../"+want+"\n")
	c, err = newContainerIndex(proc, cgroot).resolve(id)
	if err != nil {
		t.Fatal(err)
	}
	if c.pid != 40 || c.cgroup != filepath.Join(cgroot, want) {
		t.Fatalf("relative path: got pid %d cgroup %s", c.pid, c.cgroup)
	}

	// The cache follows the container: once its init is gone, it resolves
	// again and reports the container unknown.
	x := newContainerIndex(proc, cgroot)
	if _, err := x.resolve(id); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []string{"40", "41"} {
		if err := os.RemoveAll(filepath.Join(proc, pid)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := x.resolve(id); !errors.Is(err, ErrUnknownContainer) {
		t.Fatalf("after the container is gone: %v", err)
	}
}

func TestEnvironOf(t *testing.T) {
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "7", "environ"), []byte("A=1\x00B=two=2\x00\x00junk\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(environOf(proc, 7), ";")
	if got != "A=1;B=two=2" {
		t.Fatalf("environ: %q", got)
	}
}
