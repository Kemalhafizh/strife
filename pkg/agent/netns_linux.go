package agent

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// inNetns runs fn on an OS thread switched into the network namespace of
// pid, then switches the thread back. Netlink sockets fn opens belong to
// that namespace, and so do the processes it starts (the iptables
// fallback). It costs one goroutine, two setns(2) calls and two opens.
func inNetns(pid int, fn func() error) error {
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
		target, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
		if err != nil {
			runtime.UnlockOSThread()
			errc <- fmt.Errorf("agent: network namespace of pid %d: %w", pid, err)
			return
		}
		defer target.Close()
		if err := setns(target.Fd()); err != nil {
			runtime.UnlockOSThread()
			errc <- fmt.Errorf("agent: entering the network namespace of pid %d: %w", pid, err)
			return
		}
		ferr := fn()
		if err := setns(own.Fd()); err != nil {
			// The thread is stuck in the target's namespace. Leaving it
			// locked makes the runtime discard it when this goroutine
			// ends, so no other goroutine ever runs in that namespace.
			errc <- errors.Join(ferr, fmt.Errorf("agent: leaving the network namespace of pid %d: %w", pid, err))
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
