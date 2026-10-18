package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// helperArg makes the agent's binary, started by RestartIn inside a
// container's namespaces, take on the container's credentials and exec the
// start command (see RunHelper).
const helperArg = "__strife-agent-start-in-container"

// moveArg makes the agent's binary move itself into a cgroup and exec the
// rest of its arguments: on kernels that cannot clone into a cgroup, so
// that nsenter and all it starts are born there.
const moveArg = "__strife-agent-move-into-cgroup"

// cloneIntoCgroup is false to exercise the fallback for kernels before 5.7
// (tests).
var cloneIntoCgroup = true

// helperTimeout bounds how long RestartIn waits for the helper to report
// that the start command is about to run.
var helperTimeout = 10 * time.Second

// creds is what a container's init process runs as. A command started on
// the container's behalf gets the same: not the agent's root and full
// capabilities, which would bring a database back more privileged than its
// pod spec allows (and leave root-owned files behind).
type creds struct {
	UID        int    `json:"uid"`
	GID        int    `json:"gid"`
	Groups     []int  `json:"groups"`
	CapBnd     uint64 `json:"cap_bnd"`
	CapPrm     uint64 `json:"cap_prm"`
	CapEff     uint64 `json:"cap_eff"`
	CapInh     uint64 `json:"cap_inh"`
	CapAmb     uint64 `json:"cap_amb"`
	NoNewPrivs bool   `json:"no_new_privs"`
}

// credsOf reads pid's effective IDs, groups, capability sets and
// no_new_privs flag, with IDs as the agent's user namespace sees them.
func credsOf(proc string, pid int) (creds, error) {
	f, err := os.Open(filepath.Join(proc, strconv.Itoa(pid), "status"))
	if err != nil {
		return creds{}, err
	}
	defer f.Close()
	var c creds
	capSets := map[string]*uint64{"CapBnd": &c.CapBnd, "CapPrm": &c.CapPrm, "CapEff": &c.CapEff, "CapInh": &c.CapInh, "CapAmb": &c.CapAmb}
	missing := map[string]bool{"Uid": true, "Gid": true, "CapBnd": true, "CapPrm": true, "CapEff": true, "CapInh": true, "CapAmb": true}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(val)
		switch {
		case key == "Uid" || key == "Gid":
			if len(fields) < 2 {
				return creds{}, fmt.Errorf("%s: malformed %s line", f.Name(), key)
			}
			id := &c.UID
			if key == "Gid" {
				id = &c.GID
			}
			*id, err = strconv.Atoi(fields[1]) // effective
		case key == "Groups":
			for _, g := range fields {
				var id int
				if id, err = strconv.Atoi(g); err != nil {
					break
				}
				c.Groups = append(c.Groups, id)
			}
		case capSets[key] != nil:
			if len(fields) != 1 {
				return creds{}, fmt.Errorf("%s: malformed %s line", f.Name(), key)
			}
			*capSets[key], err = strconv.ParseUint(fields[0], 16, 64)
		case key == "NoNewPrivs":
			c.NoNewPrivs = len(fields) == 1 && fields[0] == "1"
		}
		if err != nil {
			return creds{}, fmt.Errorf("%s: %s: %w", f.Name(), key, err)
		}
		delete(missing, key)
	}
	if err := sc.Err(); err != nil {
		return creds{}, err
	}
	if len(missing) > 0 {
		return creds{}, fmt.Errorf("%s: no %d of Uid, Gid and the capability sets", f.Name(), len(missing))
	}
	return c, nil
}

// mapID translates id, as the agent's user namespace sees it, into pid's
// user namespace through its uid_map or gid_map.
func mapID(proc string, pid int, file string, id int) (int, error) {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), file))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		var inside, outside, count int
		if n, _ := fmt.Sscan(line, &inside, &outside, &count); n == 3 && id >= outside && id < outside+count {
			return inside + id - outside, nil
		}
	}
	return 0, fmt.Errorf("id %d has no mapping in the user namespace of pid %d", id, pid)
}

// toNamespace rewrites c's IDs for pid's user namespace.
func (c *creds) toNamespace(proc string, pid int) error {
	var err error
	if c.UID, err = mapID(proc, pid, "uid_map", c.UID); err != nil {
		return err
	}
	if c.GID, err = mapID(proc, pid, "gid_map", c.GID); err != nil {
		return err
	}
	groups := c.Groups[:0]
	for _, g := range c.Groups {
		if m, err := mapID(proc, pid, "gid_map", g); err == nil {
			groups = append(groups, m) // an unmapped group cannot be held there
		}
	}
	c.Groups = groups
	return nil
}

// RestartIn runs startCmd inside container c as if it ran there: in its
// namespaces (through nsenter), in its working directory, with its init's
// environment and credentials (user, groups, capabilities, no_new_privs),
// and in its cgroup, so the processes it starts belong to the container and
// the next pause, kill or freeze reaches them. The container's seccomp and
// LSM profiles are not applied.
func (p *ProcessEngine) RestartIn(startCmd string, c *container) error {
	if startCmd == "" {
		return fmt.Errorf("process: restart start_cmd cannot be empty")
	}
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return fmt.Errorf("process: restarting inside a container needs nsenter (util-linux): %w", err)
	}
	cr, err := credsOf("/proc", c.pid)
	if err != nil {
		return fmt.Errorf("process: credentials of container %.12s: %w", c.id, err)
	}
	args := []string{"--target", strconv.Itoa(c.pid), "--mount", "--uts", "--ipc", "--net", "--pid", "--wd"}
	if !sameNamespace("/proc", c.pid, "cgroup") {
		args = append(args, "--cgroup")
	}
	if !sameNamespace("/proc", c.pid, "user") {
		// nsenter becomes root of the container's user namespace; the
		// helper then switches to the init's IDs as that namespace sees them.
		args = append(args, "--user")
		if err := cr.toNamespace("/proc", c.pid); err != nil {
			return fmt.Errorf("process: credentials of container %.12s: %w", c.id, err)
		}
	}
	spec, err := json.Marshal(cr)
	if err != nil {
		return err
	}
	// The helper is this binary, handed over as fd 3: the container's
	// filesystem does not have it. It reports on fd 4.
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("process: opening the agent's own binary: %w", err)
	}
	defer self.Close()
	args = append(args, "--", "/proc/self/fd/3", helperArg, string(spec), startCmd)

	// Never nil: a nil environment would hand the agent's own (its token
	// included) to the database.
	env := environOf("/proc", c.pid)
	if env == nil {
		env = []string{}
	}
	start := func(cgroupFD bool) (*exec.Cmd, *os.File, error) {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, nil, err
		}
		defer w.Close()
		cmd := exec.Command(nsenter, args...)
		switch {
		case c.cgroup == "":
		case cgroupFD:
			dir, err := os.Open(c.cgroup)
			if err != nil {
				r.Close()
				return nil, nil, err
			}
			defer dir.Close()
			cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
		default:
			// Kernels before 5.7 cannot clone into a cgroup: this binary
			// moves itself there and becomes nsenter (see RunHelper).
			cmd = exec.Command("/proc/self/exe", append([]string{moveArg, filepath.Join(c.cgroup, "cgroup.procs"), nsenter}, args...)...)
		}
		cmd.Env = env
		cmd.ExtraFiles = []*os.File{self, w}
		if err := cmd.Start(); err != nil {
			r.Close()
			return nil, nil, err
		}
		return cmd, r, nil
	}
	cmd, status, err := start(cloneIntoCgroup)
	if err != nil && c.cgroup != "" && cloneIntoCgroup {
		cmd, status, err = start(false)
	}
	if err != nil {
		return fmt.Errorf("process: restarting in container %.12s: %w", c.id, err)
	}
	defer status.Close()
	exited := make(chan error, 1)
	// Reap nsenter once the start script returns; what it launched in the
	// background is re-parented to the container's init.
	go func() { exited <- cmd.Wait() }()
	if err := awaitHelper(status, exited); err != nil {
		return fmt.Errorf("process: restarting in container %.12s: %w", c.id, err)
	}
	return nil
}

// awaitHelper waits for the helper's word on status: "ok" just before it
// execs the start command, or why it could not. nsenter failing before
// the helper ran shows as status closing without a word.
func awaitHelper(status *os.File, exited <-chan error) error {
	_ = status.SetReadDeadline(time.Now().Add(helperTimeout))
	// One line: nsenter keeps its copy of the pipe open for as long as the
	// start command runs, so there is no end of file to wait for.
	line, err := bufio.NewReader(io.LimitReader(status, 4096)).ReadString('\n')
	if text := strings.TrimSpace(line); text == "ok" {
		return nil
	} else if text != "" {
		return errors.New(text)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("no word from the start helper within %v", helperTimeout)
	}
	select {
	case werr := <-exited:
		var exit *exec.ExitError
		switch {
		case errors.As(werr, &exit) && exit.ExitCode() == moveFailed:
			return errors.New("could not move the restart into the container's cgroup")
		case errors.As(werr, &exit) && exit.ExitCode() == execFailed:
			return errors.New("could not run nsenter from the container's cgroup")
		case errors.As(werr, &exit) && (exit.ExitCode() == 126 || exit.ExitCode() == 127):
			// nsenter could not exec the helper in the container.
			return fmt.Errorf("nsenter could not run the agent's binary inside the container (%v): it must be statically linked (CGO_ENABLED=0) and the container must mount /proc", werr)
		}
		return fmt.Errorf("nsenter exited before the start command ran: %v", werr)
	case <-time.After(time.Second):
		return errors.New("the start helper went away without a word")
	}
}

// RunHelper runs the part of RestartIn that happens inside the container,
// when this process was started for it, and then never returns. Programs
// that embed the agent (strife-agent, its tests) call it first thing.
func RunHelper() {
	switch {
	case len(os.Args) >= 4 && os.Args[1] == moveArg:
		// Writing a process ID moves every thread of the process.
		if err := os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			os.Exit(moveFailed)
		}
		_ = syscall.Exec(os.Args[3], os.Args[3:], os.Environ())
		os.Exit(execFailed)
	case len(os.Args) == 4 && os.Args[1] == helperArg:
		status := os.NewFile(4, "status")
		err := startAs(os.Args[2], os.Args[3], status)
		fmt.Fprintf(status, "start helper: %v\n", err)
		os.Exit(1)
	}
}

// Exit codes of the move into a cgroup, told apart from nsenter's own.
const (
	moveFailed = 124
	execFailed = 125
)

// Linux prctl(2) and capset(2) constants the syscall package lacks.
const (
	prCapbsetDrop     = 24
	prSetKeepCaps     = 8
	prSetNoNewPrivs   = 38
	prCapAmbient      = 47
	prCapAmbientRaise = 2
	linuxCapabilityV3 = 0x20080522
)

func prctl(op, arg uintptr) error {
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, op, arg, 0, 0, 0, 0); e != 0 {
		return e
	}
	return nil
}

// startAs takes on the credentials in spec and execs sh -c cmd. Capability
// sets and no_new_privs belong to a thread, so all of it happens on one
// locked thread, which then execs.
func startAs(spec, cmd string, status *os.File) error {
	runtime.LockOSThread()
	var c creds
	if err := json.Unmarshal([]byte(spec), &c); err != nil {
		return err
	}
	// The agent's binary and the status pipe must not leak into the
	// database.
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(int(status.Fd()))
	sh, err := exec.LookPath("sh")
	if err != nil {
		// No PATH in the container's environment: where nsenter (execvp)
		// would have looked.
		if _, serr := os.Stat("/bin/sh"); serr != nil {
			return fmt.Errorf("no sh in the container: %w", err)
		}
		sh = "/bin/sh"
	}
	for capNum := uintptr(0); capNum < 64; capNum++ {
		if c.CapBnd&(1<<capNum) != 0 {
			continue
		}
		if err := prctl(prCapbsetDrop, capNum); err == syscall.EINVAL {
			break // past the kernel's last capability
		} else if err != nil {
			return fmt.Errorf("dropping capability %d from the bounding set: %w", capNum, err)
		}
	}
	// Keep the permitted set across setuid, so that a non-root container
	// keeps the capabilities it was granted; capset trims it below.
	if err := prctl(prSetKeepCaps, 1); err != nil {
		return fmt.Errorf("keep capabilities: %w", err)
	}
	if err := syscall.Setgroups(c.Groups); err != nil {
		return fmt.Errorf("setgroups %v: %w", c.Groups, err)
	}
	if err := syscall.Setgid(c.GID); err != nil {
		return fmt.Errorf("setgid %d: %w", c.GID, err)
	}
	if err := syscall.Setuid(c.UID); err != nil {
		return fmt.Errorf("setuid %d: %w", c.UID, err)
	}
	hdr := struct {
		version uint32
		pid     int32
	}{version: linuxCapabilityV3}
	var data [2]struct{ effective, permitted, inheritable uint32 }
	for i := range data {
		shift := 32 * i
		data[i].effective = uint32(c.CapEff >> shift)
		data[i].permitted = uint32(c.CapPrm >> shift)
		data[i].inheritable = uint32((c.CapInh & c.CapBnd) >> shift)
	}
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return fmt.Errorf("capset: %w", e)
	}
	for capNum := uintptr(0); capNum < 64; capNum++ {
		if c.CapAmb&(1<<capNum) == 0 {
			continue
		}
		if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientRaise, capNum, 0, 0, 0); e != 0 {
			return fmt.Errorf("raising ambient capability %d: %w", capNum, e)
		}
	}
	if c.NoNewPrivs {
		if err := prctl(prSetNoNewPrivs, 1); err != nil {
			return fmt.Errorf("no_new_privs: %w", err)
		}
	}
	if _, err := status.WriteString("ok\n"); err != nil {
		return err
	}
	return syscall.Exec(sh, []string{"sh", "-c", cmd}, os.Environ())
}
