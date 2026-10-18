package agent

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// openNetns opens pid's network namespace and names it ("net:[inode]", as
// /proc/<pid>/ns/net reads). The open file keeps the namespace usable after
// pid exits: a pod's namespace outlives a container restarting in it.
func openNetns(pid int) (*os.File, string, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return nil, "", fmt.Errorf("agent: network namespace of pid %d: %w", pid, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, fmt.Sprintf("net:[%d]", st.Sys().(*syscall.Stat_t).Ino), nil
}

// inNetns runs fn on an OS thread switched into the network namespace open
// at ns, then switches the thread back. Netlink sockets fn opens belong to
// that namespace, and so do the processes it starts (the iptables
// fallback). It costs one goroutine, two setns(2) calls and one open.
func inNetns(ns *os.File, fn func() error) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		own, err := os.Open("/proc/thread-self/ns/net")
		if err != nil {
			runtime.UnlockOSThread()
			errc <- fmt.Errorf("agent: own network namespace: %w", err)
			return
		}
		defer own.Close()
		if err := setns(ns.Fd()); err != nil {
			runtime.UnlockOSThread()
			errc <- fmt.Errorf("agent: entering network namespace %s: %w", ns.Name(), err)
			return
		}
		ferr := fn()
		if err := setns(own.Fd()); err != nil {
			// The thread is stuck in the target's namespace. Leaving it
			// locked makes the runtime discard it when this goroutine
			// ends, so no other goroutine ever runs in that namespace.
			errc <- errors.Join(ferr, fmt.Errorf("agent: leaving network namespace %s: %w", ns.Name(), err))
			return
		}
		runtime.UnlockOSThread()
		errc <- ferr
	}()
	return <-errc
}

// sysSetns is setns(2)'s number: the standard library's syscall package
// does not export it.
var sysSetns = map[string]uintptr{
	"amd64": 308, "arm64": 268, "riscv64": 268, "loong64": 268,
	"386": 346, "arm": 375, "ppc64": 350, "ppc64le": 350, "s390x": 339,
}[runtime.GOARCH]

func setns(fd uintptr) error {
	if sysSetns == 0 {
		return fmt.Errorf("setns: unsupported architecture %s", runtime.GOARCH)
	}
	if _, _, e := syscall.RawSyscall(sysSetns, fd, syscall.CLONE_NEWNET, 0); e != 0 {
		return e
	}
	return nil
}
