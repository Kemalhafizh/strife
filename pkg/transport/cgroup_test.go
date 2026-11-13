package transport

import (
	"os"
	"path/filepath"
	"runtime"
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
			if runtime.GOOS == "windows" {
				t.Skipf("symlinks require developer mode privilege on Windows: %v", err)
			}
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

func TestResolveIODevice(t *testing.T) {
	sys := t.TempDir()
	disk := filepath.Join(sys, "devices", "pci0", "block", "sda")
	write(t, filepath.Join(disk, "dev"), "8:0\n")
	write(t, filepath.Join(disk, "sda1", "dev"), "8:1\n")
	write(t, filepath.Join(disk, "sda1", "partition"), "1\n")
	dm := filepath.Join(sys, "devices", "virtual", "block", "dm-0")
	write(t, filepath.Join(dm, "dev"), "253:0\n")
	if err := os.MkdirAll(filepath.Join(sys, "dev", "block"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"8:0":   disk,
		"8:1":   filepath.Join(disk, "sda1"),
		"253:0": dm,
	} {
		if err := os.Symlink(target, filepath.Join(sys, "dev", "block", link)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlinks require developer mode privilege on Windows: %v", err)
			}
			t.Fatal(err)
		}
	}

	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	write(t, mountInfo, ""+
		"10 1 0:43 / /var/lib/docker rw - overlay overlay rw,lowerdir=/ro,upperdir=/upper\\040dir/diff,workdir=/work\n"+
		"11 1 0:48 / /mnt/btrfs rw - btrfs /dev/sda1[/docker] rw\n"+
		"12 1 0:66 / /tmp rw - tmpfs  rw\n",
	)

	statDev := func(p string) (string, error) {
		switch p {
		case "/var/lib/docker":
			return "0:43", nil
		case "/upper dir/diff":
			return "8:1", nil
		case "/mnt/dm":
			return "253:0", nil
		case "/mnt/btrfs":
			return "0:48", nil
		case "/tmp":
			return "0:66", nil
		default:
			return "", os.ErrNotExist
		}
	}
	statBlock := func(p string) (string, error) {
		if p == "/dev/sda1" {
			return "8:1", nil
		}
		return "", os.ErrNotExist
	}

	cases := []struct {
		root string
		want string
	}{
		{root: "/mnt/dm", want: "253:0"},
		{root: "/var/lib/docker", want: "8:0"},
		{root: "/mnt/btrfs", want: "8:0"},
	}
	for _, tc := range cases {
		got, err := resolveIODevice(tc.root, mountInfo, sys, statDev, statBlock)
		if err != nil || got != tc.want {
			t.Errorf("resolveIODevice(%q) = %q, %v; want %s", tc.root, got, err, tc.want)
		}
	}
	if _, err := resolveIODevice("/tmp", mountInfo, sys, statDev, statBlock); err == nil {
		t.Error("tmpfs without backing block device must return an error")
	}
}
