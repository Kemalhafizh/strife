package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// RestartIn runs startCmd inside container c as if it ran there: in its
// namespaces (through nsenter), with its init's environment, and in its
// cgroup, so the processes it starts belong to the container and the next
// pause, kill or freeze reaches them.
func (p *ProcessEngine) RestartIn(startCmd string, c *container) error {
	if startCmd == "" {
		return fmt.Errorf("process: restart start_cmd cannot be empty")
	}
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return fmt.Errorf("process: restarting inside a container needs nsenter (util-linux): %w", err)
	}
	command := func() *exec.Cmd {
		cmd := exec.Command(nsenter, "--target", strconv.Itoa(c.pid),
			"--mount", "--uts", "--ipc", "--net", "--pid", "--", "sh", "-c", startCmd)
		cmd.Env = environOf("/proc", c.pid)
		return cmd
	}
	cmd := command()
	var dir *os.File
	if c.cgroup != "" {
		if dir, err = os.Open(c.cgroup); err == nil {
			defer dir.Close()
			cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
		}
	}
	err = cmd.Start()
	if err != nil && cmd.SysProcAttr != nil {
		// Kernels before 5.7 cannot clone into a cgroup: start the
		// command, then move it there.
		cmd = command()
		if err = cmd.Start(); err == nil {
			procs := filepath.Join(c.cgroup, "cgroup.procs")
			if werr := os.WriteFile(procs, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); werr != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return fmt.Errorf("process: moving the restart into %s: %w", c.cgroup, werr)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("process: restarting in container %.12s: %w", c.id, err)
	}
	// Reap nsenter once the start script returns; what it launched in the
	// background is re-parented to the container's init.
	go func() { _ = cmd.Wait() }()
	return nil
}
