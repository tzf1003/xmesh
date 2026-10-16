//go:build darwin || linux

package pty

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/identity"
	"golang.org/x/sys/unix"
)

func Test_JobControlInterruptAndClose(t *testing.T) {
	m, r, p, _ := fixture(t)
	c := identity.Caller{ID: "node/jobs"}
	out, e := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24})
	if e != nil {
		t.Fatal(e)
	}
	id := out["session_id"].(string)
	// A foreground process group can appear before the child installs its
	// interrupt disposition. Announce readiness after a foreground handler is
	// installed; exec then changes that caught handler to the default disposition.
	command := "set +H\n" + `set -m; sh -c 'trap "" TERM HUP; exec sleep 300' & printf 'BG_PID=%s\n' "$!"; sh -c 'trap "exit 130" INT; printf "FG_%s\n" READY; exec sleep 300'` + "\n"
	_, e = invoke(m, r, p, c, "pty.write", Object{"session_id": id, "data": command})
	if e != nil {
		t.Fatal(e)
	}
	cursor := uint64(0)
	readUntil := func(pattern *regexp.Regexp) string {
		t.Helper()
		var output strings.Builder
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			v, e := invoke(m, r, p, c, "pty.read", Object{"session_id": id, "cursor": cursor, "wait_ms": 50})
			if e != nil {
				t.Fatal(e)
			}
			cursor = v["next_cursor"].(uint64)
			output.WriteString(v["data"].(string))
			if pattern.MatchString(output.String()) {
				return output.String()
			}
		}
		t.Fatalf("terminal output deadline: %q", output.String())
		return ""
	}
	marker := regexp.MustCompile(`BG_PID=([0-9]+)[\s\S]*FG_READY`)
	output := readUntil(marker)
	background, _ := strconv.Atoi(marker.FindStringSubmatch(output)[1])
	t.Cleanup(func() { _ = syscall.Kill(background, syscall.SIGKILL) })
	m.mu.Lock()
	term := m.sessions[id].term.(*unixTerminal)
	m.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		group, _ := unix.IoctlGetInt(int(term.file.Fd()), unix.TIOCGPGRP)
		if group > 1 && group != term.PID() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("foreground job not started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, e = invoke(m, r, p, c, "pty.signal", Object{"session_id": id, "signal": "INT"}); e != nil {
		t.Fatal(e)
	}
	if _, e = invoke(m, r, p, c, "pty.write", Object{"session_id": id, "data": "printf 'SHELL_%s\\n' READY\n"}); e != nil {
		t.Fatal(e)
	}
	readUntil(regexp.MustCompile(`SHELL_READY`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args, _ := json.Marshal(Object{"session_id": id})
	if _, e = m.Call(ctx, c, r, p, "pty.close", args); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(time.Second)
	for processRunning(background) {
		if time.Now().After(deadline) {
			t.Fatal("background job survived close", background)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processRunning reports whether pid is alive. A zombie that an init process
// has not yet reaped (common in minimal containers) counts as exited.
func processRunning(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	end := strings.LastIndexByte(string(stat), ')')
	return end < 0 || len(stat) < end+3 || stat[end+2] != 'Z'
}
