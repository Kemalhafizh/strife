package transport

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupOfPID(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "42", "cgroup"), "0::/system.slice/docker-abc.scope\n")
	got, err := cgroupOfPID(proc, "/sys/fs/cgroup", 42)
	if err != nil || got != "/sys/fs/cgroup/system.slice/docker-abc.scope" {
		t.Fatalf("got %q, %v", got, err)
	}
	// cgroup v1 (or hybrid): refused with an explanation.
	write(t, filepath.Join(proc, "43", "cgroup"), "12:freezer:/docker/abc\n0::/\n")
	if _, err := cgroupOfPID(proc, "/sys/fs/cgroup", 43); err == nil {
		t.Fatal("a v1 hierarchy must be refused")
	}
}

func TestWholeDisk(t *testing.T) {
	sys := t.TempDir()
	// sda (8:0) with partition sda1 (8:1), as sysfs lays them out.
	disk := filepath.Join(sys, "devices", "pci0", "block", "sda")
	write(t, filepath.Join(disk, "dev"), "8:0\n")
	write(t, filepath.Join(disk, "sda1", "dev"), "8:1\n")
	write(t, filepath.Join(disk, "sda1", "partition"), "1\n")
	if err := os.MkdirAll(filepath.Join(sys, "dev", "block"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"8:0": disk, "8:1": filepath.Join(disk, "sda1")} {
		if err := os.Symlink(target, filepath.Join(sys, "dev", "block", link)); err != nil {
			t.Fatal(err)
		}
	}
	for in, want := range map[string]string{"8:1": "8:0", "8:0": "8:0"} {
		if got, err := wholeDisk(sys, in); err != nil || got != want {
			t.Errorf("wholeDisk(%s) = %q, %v; want %s", in, got, err, want)
		}
	}
	if _, err := wholeDisk(sys, "9:9"); err == nil {
		t.Error("unknown devices must be an error")
	}
}
