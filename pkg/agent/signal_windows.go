//go:build windows

package agent

import (
	"fmt"
	"os"
)

func sendStop(pid int) error {
	return fmt.Errorf("process: SIGSTOP not supported on windows (pid %d)", pid)
}

func sendCont(pid int) error {
	return fmt.Errorf("process: SIGCONT not supported on windows (pid %d)", pid)
}

func sendKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
