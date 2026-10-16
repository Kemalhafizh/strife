//go:build !windows

package agent

import "syscall"

func sendStop(pid int) error {
	return syscall.Kill(pid, syscall.SIGSTOP)
}

func sendCont(pid int) error {
	return syscall.Kill(pid, syscall.SIGCONT)
}

func sendKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
