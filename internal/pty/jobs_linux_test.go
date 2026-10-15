package pty

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
)

func Test_L2JobControl(t *testing.T) {
	if os.Getenv("XMESH_TEST_L2") != "1" {
		t.Skip("set XMESH_TEST_L2=1 on Linux with bubblewrap, util-linux and user namespaces")
	}
	m, r, _, _ := fixture(t)
	p, err := policy.New(config.Policy{Isolation: "L2"})
	if err != nil {
		t.Fatal(err)
	}
	c := identity.Caller{ID: "node/l2-jobs"}
	out, err := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24})
	if err != nil {
		t.Fatal(err)
	}
	id := out["session_id"].(string)
	launcher := out["pid"].(int)
	cursor := uint64(0)
	var transcript strings.Builder
	write := func(command string) {
		t.Helper()
		if _, err := invoke(m, r, p, c, "pty.write", Object{"session_id": id, "data": command}); err != nil {
			t.Fatal(err)
		}
	}
	readUntil := func(pattern string) {
		t.Helper()
		var output strings.Builder
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			v, err := invoke(m, r, p, c, "pty.read", Object{"session_id": id, "cursor": cursor, "wait_ms": 50})
			if err != nil {
				t.Fatal(err)
			}
			cursor = v["next_cursor"].(uint64)
			data := v["data"].(string)
			transcript.WriteString(data)
			output.WriteString(data)
			if regexp.MustCompile(pattern).MatchString(output.String()) {
				return
			}
		}
		t.Fatalf("terminal output deadline for %s: %q", pattern, transcript.String())
	}
	// A namespace-local shell must have working job control and a controlling
	// terminal; stty also verifies that the inherited slave has the requested size.
	write("set -m; printf 'SIZE='; stty size; printf 'INITIAL_%s\\n' READY\n")
	readUntil(`INITIAL_READY`)
	if !strings.Contains(transcript.String(), "SIZE=24 80") || strings.Contains(transcript.String(), "job control turned off") {
		t.Fatal(transcript.String())
	}
	if _, err := invoke(m, r, p, c, "pty.resize", Object{"session_id": id, "cols": 101, "rows": 37}); err != nil {
		t.Fatal(err)
	}
	write("printf 'RESIZED='; stty size\n")
	readUntil(`RESIZED=37 101`)

	write("sh -c 'trap \"\" TERM HUP; exec sleep 301' & printf 'BACKGROUND_%s\\n' READY; sleep 302\n")
	readUntil(`BACKGROUND_READY`)
	background := waitSandboxProcess(t, launcher, "sleep\x00301\x00")
	foreground := waitSandboxProcess(t, launcher, "sleep\x00302\x00")
	// These are host PIDs discovered under this launcher's process tree. PIDs
	// printed by the shell belong to the inner namespace and must not be signaled.
	t.Cleanup(func() {
		_ = syscall.Kill(background, syscall.SIGKILL)
		_ = syscall.Kill(foreground, syscall.SIGKILL)
	})
	if _, err := invoke(m, r, p, c, "pty.signal", Object{"session_id": id, "signal": "INT"}); err != nil {
		t.Fatal(err)
	}
	write("printf 'SIGNAL_%s\\n' READY\n")
	readUntil(`SIGNAL_READY`)
	write("sleep 303\n")
	_ = waitSandboxProcess(t, launcher, "sleep\x00303\x00")
	write("\x03")
	write("printf 'KEYBOARD_%s\\n' READY\n")
	readUntil(`KEYBOARD_READY`)
	if _, err := invoke(m, r, p, c, "pty.close", Object{"session_id": id}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		if !liveSandboxProcess(background) && !liveSandboxProcess(foreground) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sandbox jobs survived close", background, foreground)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(transcript.String(), "Cannot set tty process group") {
		t.Fatal(transcript.String())
	}
}

func waitSandboxProcess(t *testing.T, parent int, command string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		for _, pid := range sandboxDescendants(parent) {
			data, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
			if string(data) == command {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("sandbox process not started", command)
	return 0
}

func sandboxDescendants(parent int) []int {
	data, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(parent), "task", strconv.Itoa(parent), "children"))
	var result []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err == nil {
			result = append(result, pid)
			result = append(result, sandboxDescendants(pid)...)
		}
	}
	return result
}

func liveSandboxProcess(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data)[strings.LastIndexByte(string(data), ')')+1:])
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}
