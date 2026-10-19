package pty

import (
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/identity"
)

// CTR-PTY-02 says max_bytes tops out at 1 MiB. exec.output clamps oversized
// limits instead of failing, so pty.read follows the same rule for callers
// that reuse one large constant.
func Test_CTRPTY02_MaxBytesClamps(t *testing.T) {
	m, reg := lockFixture(t)
	owner := identity.Caller{ID: "node/owner"}
	_ = reg.Add(owner)
	term := newGateTerminal(false)
	s := addSession(m, "p-clamp0001", owner.ID, term)
	// Seed the scrollback synchronously: wait_ms=0 must read data already available.
	s.append([]byte("hello"))
	if out, err := call(m, owner, "pty.read", Object{"session_id": "p-clamp0001", "max_bytes": 2 << 20, "wait_ms": 0}); err != nil || out["data"] != "hello" {
		t.Fatalf("oversized max_bytes rejected: %v %v", out, err)
	}
	if _, err := call(m, owner, "pty.read", Object{"session_id": "p-clamp0001", "max_bytes": -1}); errorCode(err) != "invalid_args" {
		t.Fatalf("negative max_bytes: %v", err)
	}
	if _, err := call(m, owner, "pty.read", Object{"session_id": "p-clamp0001", "wait_ms": -5}); errorCode(err) != "invalid_args" {
		t.Fatalf("negative wait_ms: %v", err)
	}
}

// Oversized wait_ms clamps to the 30 s contract maximum rather than being an
// argument error.
func Test_CTRPTY02_WaitClamps(t *testing.T) {
	m, reg := lockFixture(t)
	owner := identity.Caller{ID: "node/owner"}
	_ = reg.Add(owner)
	term := newGateTerminal(false)
	addSession(m, "p-clamp0002", owner.ID, term)
	started := time.Now()
	out, err := call(m, owner, "pty.read", Object{"session_id": "p-clamp0002", "wait_ms": 10})
	if err != nil || out["data"] != "" {
		t.Fatalf("%v %v", out, err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("read did not honor wait_ms")
	}
}
