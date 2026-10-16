package pty

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
)

// lockBound is deliberately generous: it separates "completes" from
// "blocked until the test releases it", never from "merely slow".
const lockBound = 10 * time.Second

// gateTerminal is an in-memory terminal. Output is injected through feed,
// and Write can be held at a gate to simulate a stuck terminal input pipe.
type gateTerminal struct {
	feed     chan []byte
	closed   chan struct{}
	once     sync.Once
	gateOnce sync.Once
	gate     chan struct{} // nil: Write never blocks
	entered  chan struct{} // receives one value each time Write is entered

	mu      sync.Mutex // guards the recorders below
	writes  []string
	resizes [][2]int
	signals []string
}

func newGateTerminal(gated bool) *gateTerminal {
	g := &gateTerminal{feed: make(chan []byte, 16), closed: make(chan struct{}), entered: make(chan struct{}, 16)}
	if gated {
		g.gate = make(chan struct{})
	}
	return g
}
func (g *gateTerminal) Read(b []byte) (int, error) {
	select {
	case data := <-g.feed:
		return copy(b, data), nil
	case <-g.closed:
		return 0, io.EOF
	}
}
func (g *gateTerminal) Write(p []byte) (int, error) {
	g.mu.Lock()
	g.writes = append(g.writes, string(p))
	g.mu.Unlock()
	select {
	case g.entered <- struct{}{}:
	default:
	}
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-g.closed:
		}
	}
	return len(p), nil
}
func (g *gateTerminal) release()     { g.once.Do(func() { close(g.closed) }) }
func (g *gateTerminal) Close() error { g.release(); return nil }
func (g *gateTerminal) Resize(c, r int) error {
	g.mu.Lock()
	g.resizes = append(g.resizes, [2]int{c, r})
	g.mu.Unlock()
	return nil
}
func (g *gateTerminal) Signal(s string) error {
	g.mu.Lock()
	g.signals = append(g.signals, s)
	g.mu.Unlock()
	g.release()
	return nil
}
func (g *gateTerminal) effects() (writes []string, resizes [][2]int, signals []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string{}, g.writes...), append([][2]int{}, g.resizes...), append([]string{}, g.signals...)
}
func (g *gateTerminal) Wait() error        { <-g.closed; return nil }
func (*gateTerminal) PID() int             { return 4321 }
func (g *gateTerminal) unblockWrites()     { g.gateOnce.Do(func() { close(g.gate) }) }
func (g *gateTerminal) output(text string) { g.feed <- []byte(text) }
func (g *gateTerminal) waitWriteEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(lockBound):
		t.Fatal("terminal write was never entered")
	}
}

// lockFixture builds a Manager whose sessions are backed by gateTerminal and
// pumped exactly like real sessions.
func lockFixture(t *testing.T) (*Manager, *identity.Registry) {
	t.Helper()
	reg := identity.NewRegistry()
	m := New(config.PTY{MaxSessions: 20, MaxPerCaller: 20, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "auto", ClosedRetain: "5m", ClosedRetainMax: 20}, reg)
	t.Cleanup(m.Shutdown)
	return m, reg
}
func addSession(m *Manager, id, owner string, term terminal) *Session {
	s := &Session{ID: id, Owner: owner, Node: identity.Node(owner), State: "active", term: term, capacity: 1 << 16, changed: make(chan struct{}), started: make(chan struct{})}
	s.touch()
	close(s.started)
	m.mu.Lock()
	m.sessions[id] = s
	m.byCaller[owner]++
	m.mu.Unlock()
	go m.pump(s)
	return s
}
func call(m *Manager, c identity.Caller, name string, args Object) (Object, error) {
	b, _ := json.Marshal(args)
	return m.Call(context.Background(), c, nil, nil, name, b)
}

// within runs fn and fails the test if it does not return in time. The
// function keeps running in the background if it is blocked, so the caller
// must release whatever it is blocked on before the test ends.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(lockBound):
		t.Fatalf("%s did not complete while an unrelated operation was in progress", what)
	}
}

// waitBlockedInRead returns once some goroutine is parked in Session.read's
// select (a long-poll with no data), so the test never races the long-poll.
func waitBlockedInRead(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(lockBound)
	buf := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, "pty.(*Session).read(") && strings.Contains(g, "[select") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no goroutine parked in pty.read long-poll")
}

// CON-IMPL-03: a pty.read long-poll (waiting on the session's condition) holds
// neither the Manager lock nor the owner-operation lock, so write, list and
// further reads on the same and other sessions proceed.
// conformance: CON-IMPL-03
func Test_CONIMPL03_LongPollDoesNotBlockWriteListOpen(t *testing.T) {
	m, reg := lockFixture(t)
	owner := identity.Caller{ID: "node/owner"}
	_ = reg.Add(owner)
	termA, termB := newGateTerminal(false), newGateTerminal(false)
	addSession(m, "p-aaaaaaaa", owner.ID, termA)
	addSession(m, "p-bbbbbbbb", owner.ID, termB)

	type readResult struct {
		out Object
		err error
	}
	polled := make(chan readResult, 1)
	go func() {
		out, err := call(m, owner, "pty.read", Object{"session_id": "p-aaaaaaaa", "cursor": 0, "wait_ms": 30000})
		polled <- readResult{out, err}
	}()
	waitBlockedInRead(t)

	within(t, "pty.write on the polled session", func() {
		if _, err := call(m, owner, "pty.write", Object{"session_id": "p-aaaaaaaa", "data": "x"}); err != nil {
			t.Error(err)
		}
	})
	within(t, "pty.write on another session", func() {
		if _, err := call(m, owner, "pty.write", Object{"session_id": "p-bbbbbbbb", "data": "x"}); err != nil {
			t.Error(err)
		}
	})
	within(t, "pty.list", func() {
		if out, err := call(m, owner, "pty.list", Object{}); err != nil || len(out["sessions"].([]Object)) != 2 {
			t.Error(out, err)
		}
	})
	within(t, "a second long-poll with wait_ms=0 on the polled session", func() {
		if out, err := call(m, owner, "pty.read", Object{"session_id": "p-aaaaaaaa", "cursor": 0, "wait_ms": 0}); err != nil || out["data"] != "" {
			t.Error(out, err)
		}
	})
	within(t, "Counts", func() { m.Counts() })
	if runtime.GOOS != "windows" {
		// pty.open needs a real shell; the fake terminals cover everything else.
		root := t.TempDir()
		resolver, err := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
		if err != nil {
			t.Fatal(err)
		}
		engine, _ := policy.New(config.Policy{})
		within(t, "pty.open", func() {
			cfg := config.PTY{MaxSessions: 20, MaxPerCaller: 20, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "/bin/sh", ClosedRetain: "5m", ClosedRetainMax: 20}
			m.Configure(cfg)
			args, _ := json.Marshal(Object{"cols": 80, "rows": 24})
			if _, err := m.Call(context.Background(), owner, resolver, engine, "pty.open", args); err != nil {
				t.Error(err)
			}
		})
	}
	select {
	case r := <-polled:
		t.Fatal("long-poll returned without any output", r)
	default:
	}

	// The long-poll is still serviced: output produced after all of the above arrives.
	termA.output("hello")
	select {
	case r := <-polled:
		if r.err != nil || r.out["data"] != "hello" {
			t.Fatal(r.out, r.err)
		}
	case <-time.After(lockBound):
		t.Fatal("long-poll never woke up for new output")
	}
}

// CON-IMPL-03: a write that is stuck in the terminal holds only that
// session's owner-operation lock; read (no owner check, ring lock only) and
// list on the same session, and every operation on other sessions, still run.
// conformance: CON-IMPL-03
func Test_CONIMPL03_BlockedWriteDoesNotBlockReadListOtherSessions(t *testing.T) {
	m, reg := lockFixture(t)
	owner := identity.Caller{ID: "node/owner"}
	_ = reg.Add(owner)
	stuck, free := newGateTerminal(true), newGateTerminal(false)
	addSession(m, "p-stuckaaa", owner.ID, stuck)
	addSession(m, "p-freebbbb", owner.ID, free)
	free.output("seed")

	writing := make(chan error, 1)
	go func() {
		_, err := call(m, owner, "pty.write", Object{"session_id": "p-stuckaaa", "data": "blocked"})
		writing <- err
	}()
	stuck.waitWriteEntered(t)
	t.Cleanup(func() {
		select {
		case <-stuck.closed:
		default:
			stuck.unblockWrites()
		}
	})

	within(t, "pty.read on the session with the stuck write", func() {
		if out, err := call(m, owner, "pty.read", Object{"session_id": "p-stuckaaa", "wait_ms": 0}); err != nil || out["eof"] != false {
			t.Error(out, err)
		}
	})
	within(t, "pty.list", func() {
		if out, err := call(m, owner, "pty.list", Object{}); err != nil || len(out["sessions"].([]Object)) != 2 {
			t.Error(out, err)
		}
	})
	within(t, "pty.write on another session", func() {
		if _, err := call(m, owner, "pty.write", Object{"session_id": "p-freebbbb", "data": "x"}); err != nil {
			t.Error(err)
		}
	})
	within(t, "pty.read with data on another session", func() {
		deadline := time.Now().Add(lockBound)
		for time.Now().Before(deadline) {
			out, err := call(m, owner, "pty.read", Object{"session_id": "p-freebbbb", "wait_ms": 100})
			if err != nil {
				t.Error(err)
				return
			}
			if out["data"] == "seed" {
				return
			}
		}
		t.Error("seed output never read")
	})
	select {
	case err := <-writing:
		t.Fatal("stuck write returned early", err)
	default:
	}
	stuck.unblockWrites()
	select {
	case err := <-writing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(lockBound):
		t.Fatal("write did not finish after the terminal accepted input")
	}
}

// CON-IMPL-03: the Manager mutex and a session's ring mutex are independent:
// producing and reading output needs no Manager lock, and Manager operations
// that do not touch a session's ring need no ring lock.
// conformance: CON-IMPL-03
func Test_CONIMPL03_ManagerLockAndRingLockIndependent(t *testing.T) {
	m, reg := lockFixture(t)
	owner := identity.Caller{ID: "node/owner"}
	_ = reg.Add(owner)
	term := newGateTerminal(false)
	s := addSession(m, "p-ringlock", owner.ID, term)

	// Hold the Manager lock: ring appends and ring reads proceed.
	m.mu.Lock()
	unlocked := false
	t.Cleanup(func() {
		if !unlocked {
			m.mu.Unlock()
		}
	})
	within(t, "ring append while the Manager lock is held", func() { s.append([]byte("abc")) })
	within(t, "ring read while the Manager lock is held", func() {
		out, err := s.read(context.Background(), 0, 16, 0)
		if err != nil || out["data"] != "abc" || out["next_cursor"] != uint64(3) {
			t.Error(out, err)
		}
	})
	m.mu.Unlock()
	unlocked = true

	// Hold the ring lock: Manager-level operations proceed.
	s.mu.Lock()
	ringHeld := true
	t.Cleanup(func() {
		if ringHeld {
			s.mu.Unlock()
		}
	})
	within(t, "pty.list while a ring lock is held", func() { m.List() })
	within(t, "Counts while a ring lock is held", func() { m.Counts() })
	within(t, "pty.write while a ring lock is held", func() {
		if _, err := call(m, owner, "pty.write", Object{"session_id": "p-ringlock", "data": "x"}); err != nil {
			t.Error(err)
		}
	})
	s.mu.Unlock()
	ringHeld = false
}
