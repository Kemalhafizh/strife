//go:build windows || js || wasip1

package agent

import (
	"fmt"
	"os"
	"runtime"
)

func sendStop(pid int) error {
	return fmt.Errorf("process: SIGSTOP not supported on %s (pid %d)", runtime.GOOS, pid)
}

func sendCont(pid int) error {
	return fmt.Errorf("process: SIGCONT not supported on %s (pid %d)", runtime.GOOS, pid)
}

func sendKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
