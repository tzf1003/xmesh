//go:build linux

package delegateguard

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

// ReaperMarker selects the Linux subreaper wrapper entrypoint when re-executing
// the host binary for a delegated task.
const ReaperMarker = "__delegate-reaper"

var subreaperEnabled atomic.Bool

// EnableSubreaper enables wrapping non-L2 delegated tasks in a PR_SET_CHILD_SUBREAPER
// helper so double-fork + setsid descendants cannot reparent to PID 1.
func EnableSubreaper() { subreaperEnabled.Store(true) }

// SubreaperEnabled reports whether EnableSubreaper was called.
func SubreaperEnabled() bool { return subreaperEnabled.Load() }

// HandleReaper enables subreaper support for the current binary and executes
// the subreaper helper when args[1] == ReaperMarker. TestMain hooks call this
// alongside delegatetest.Serve.
func HandleReaper(args []string) {
	subreaperEnabled.Store(true)
	if len(args) < 2 || args[1] != ReaperMarker {
		return
	}
	os.Exit(RunReaper(args[2:]))
}

// ReaperPipes holds the death-notification and startup-ack pipe ends for a
// wrapped subreaper child.
type ReaperPipes struct {
	DeathRead  *os.File
	DeathWrite *os.File
	StartRead  *os.File
	StartWrite *os.File
}

// CloseChildEnds closes the parent's copies of child-bound descriptors after cmd.Start.
func (p *ReaperPipes) CloseChildEnds() {
	if p == nil {
		return
	}
	closePipe(&p.DeathRead)
	closePipe(&p.StartWrite)
}

// Apply attaches the reaper's pipe ends to cmd.ExtraFiles before cmd.Start.
func (p *ReaperPipes) Apply(cmd *exec.Cmd) {
	if p == nil {
		return
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.DeathRead, p.StartWrite)
}

// CloseAll closes every pipe descriptor on startup failure.
func (p *ReaperPipes) CloseAll() {
	if p == nil {
		return
	}
	closePipe(&p.DeathRead)
	closePipe(&p.DeathWrite)
	closePipe(&p.StartRead)
	closePipe(&p.StartWrite)
}

// WaitStart waits for the subreaper to launch its target command, returning any start error.
func (p *ReaperPipes) WaitStart() error {
	if p == nil || p.StartRead == nil {
		return nil
	}
	defer closePipe(&p.StartRead)
	buf, _ := io.ReadAll(io.LimitReader(p.StartRead, 4096))
	if len(buf) > 0 {
		return errors.New(string(buf))
	}
	return nil
}

// CloseDeath signals the subreaper (via EOF on FD 3) to clean up its subtree.
func (p *ReaperPipes) CloseDeath() {
	if p == nil {
		return
	}
	closePipe(&p.DeathWrite)
}

func closePipe(f **os.File) {
	if *f != nil {
		_ = (*f).Close()
		*f = nil
	}
}

// WrapCommand wraps cmd and args with the subreaper entrypoint when enabled.
func WrapCommand(cmd string, args []string) (launch string, launchArgs []string, pipes *ReaperPipes, ok bool) {
	if !subreaperEnabled.Load() {
		return "", nil, nil, false
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "", nil, nil, false
	}
	dr, dw, err := os.Pipe()
	if err != nil {
		return "", nil, nil, false
	}
	sr, sw, err := os.Pipe()
	if err != nil {
		_ = dr.Close()
		_ = dw.Close()
		return "", nil, nil, false
	}
	out := make([]string, 0, len(args)+3)
	out = append(out, ReaperMarker, "--", cmd)
	out = append(out, args...)
	return exe, out, &ReaperPipes{DeathRead: dr, DeathWrite: dw, StartRead: sr, StartWrite: sw}, true
}

// RunReaper acts as a PR_SET_CHILD_SUBREAPER parent for one delegated task.
func RunReaper(args []string) int {
	// Ensure target child doesn't keep FD 4 open after execve, while we can write
	// start errors before exiting if cmd.Start fails.
	unix.CloseOnExec(4)
	startStatus := os.NewFile(4, "start-status")
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" {
		if startStatus != nil {
			_, _ = startStatus.WriteString("missing command")
			_ = startStatus.Close()
		}
		return 2
	}
	_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		if startStatus != nil {
			_, _ = startStatus.WriteString(err.Error())
			_ = startStatus.Close()
		}
		return 127
	}
	if startStatus != nil {
		_ = startStatus.Close()
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	// ExtraFiles[0] in parent becomes FD 3 in this process. When parent exits or
	// closes its write end, EOF triggers cleanup of the entire subtree.
	death := os.NewFile(3, "death-pipe")
	deathClosed := make(chan struct{})
	go func() {
		defer close(deathClosed)
		if death != nil {
			var buf [1]byte
			_, _ = death.Read(buf[:])
			_ = death.Close()
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var waitErr error
	running := true
	for running {
		select {
		case waitErr = <-done:
			running = false
		case sig := <-sigs:
			s := syscall.SIGTERM
			if sig == syscall.SIGINT {
				s = syscall.SIGINT
			}
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
			killDescendants(s)
		case <-deathClosed:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			killDescendants(syscall.SIGKILL)
			deathClosed = nil
		}
	}

	killDescendants(syscall.SIGKILL)
	reapAll()

	if waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && exitErr.ProcessState != nil {
		if ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exitErr.ProcessState.ExitCode()
	}
	return 1
}

func killDescendants(sig syscall.Signal) {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	children := map[int][]int{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		children[ppid] = append(children[ppid], pid)
	}
	queue := append([]int(nil), children[self]...)
	seen := map[int]bool{self: true}
	var descendants []int
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		descendants = append(descendants, pid)
		queue = append(queue, children[pid]...)
	}
	for i := len(descendants) - 1; i >= 0; i-- {
		_ = syscall.Kill(descendants[i], sig)
	}
}

func reapAll() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}
