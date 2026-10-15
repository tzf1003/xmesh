//go:build !windows

package pty

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Manager, *cwd.Resolver, *policy.Engine, *identity.Registry) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	r, _ := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
	p, _ := policy.New(config.Policy{})
	reg := identity.NewRegistry()
	m := New(config.PTY{MaxSessions: 20, MaxPerCaller: 30, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "/bin/sh", ClosedRetain: "5m", ClosedRetainMax: 20}, reg)
	t.Cleanup(m.Shutdown)
	return m, r, p, reg
}
func invoke(m *Manager, r *cwd.Resolver, p *policy.Engine, c identity.Caller, name string, a Object) (Object, error) {
	b, _ := json.Marshal(a)
	return m.Call(context.Background(), c, r, p, name, b)
}
func Test_CONCONC03_20Concurrent(t *testing.T) {
	m, r, p, _ := fixture(t)
	c := identity.Caller{ID: "node/session"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, limited := 0, 0
	for i := 0; i < 21; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24})
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				success++
			} else if v, ok := e.(*toolerr.Error); ok && v.Code == "pty_limit" {
				limited++
				if v.Fields["in_use"] != 20 {
					t.Errorf("count: %v", v.Fields)
				}
			} else {
				t.Errorf("open: %v (%v)", e, e.(*toolerr.Error).Cause)
			}
		}()
	}
	wg.Wait()
	if success != 20 || limited != 1 {
		t.Fatalf("success %d limited %d", success, limited)
	}
	in, active, _ := m.Counts()
	if in != 20 || active != 20 {
		t.Fatal(in, active)
	}
}
func Test_CTRPTY02_RingAndLongPoll(t *testing.T) {
	s := &Session{capacity: 8, changed: make(chan struct{})}
	s.touch()
	s.append([]byte("abcdefghijkl"))
	o, e := s.read(context.Background(), 0, 4, 0)
	if e != nil || o["data"] != "efgh" || o["next_cursor"] != uint64(8) || o["truncated"] != true {
		t.Fatal(o, e)
	}
	now := time.Now()
	o, e = s.read(context.Background(), 12, 4, 20*time.Millisecond)
	if e != nil || o["data"] != "" || time.Since(now) < 15*time.Millisecond {
		t.Fatal(o, e)
	}
	go func() { time.Sleep(10 * time.Millisecond); s.append([]byte("Z")) }()
	o, e = s.read(context.Background(), 12, 4, time.Second)
	if e != nil || o["data"] != "Z" {
		t.Fatal(o, e)
	}
}
func Test_CTRPTY01_ExitDrainAndQuota(t *testing.T) {
	m, r, p, _ := fixture(t)
	c := identity.Caller{ID: "node/session"}
	o, e := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24})
	if e != nil {
		t.Fatal(e)
	}
	id := o["session_id"].(string)
	_, e = invoke(m, r, p, identity.Caller{ID: "other/session"}, "pty.write", Object{"session_id": id, "data": "exit\n"})
	if e == nil {
		t.Fatal("nonowner write")
	}
	if _, e = invoke(m, r, p, c, "pty.write", Object{"session_id": id, "data": "printf 'FINAL_MARKER\\n'; exit\n"}); e != nil {
		t.Fatal(e)
	}
	cursor := uint64(0)
	var text strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		o, e = invoke(m, r, p, c, "pty.read", Object{"session_id": id, "cursor": cursor, "wait_ms": 100})
		if e != nil {
			t.Fatal(e)
		}
		text.WriteString(o["data"].(string))
		cursor = o["next_cursor"].(uint64)
		if o["eof"] == true {
			break
		}
	}
	if o["eof"] != true || !strings.Contains(text.String(), "FINAL_MARKER") {
		t.Fatal(o, text.String())
	}
	in, active, closed := m.Counts()
	if in != 0 || active != 0 || closed != 1 {
		t.Fatal(in, active, closed)
	}
	if _, e = invoke(m, r, p, c, "pty.close", Object{"session_id": id}); e != nil {
		t.Fatal(e)
	}
	m.Reap(time.Now().Add(6 * time.Minute))
	if len(m.List()) != 0 {
		t.Fatal("retained expired")
	}
}
func Test_RUL13_OrphanOwnership(t *testing.T) {
	m, r, p, reg := fixture(t)
	a := identity.Caller{ID: "node/a", OAuthClient: "client-a"}
	b := identity.Caller{ID: "node/b", OAuthClient: "client-b"}
	c := identity.Caller{ID: "node/c", OAuthClient: "client-a"}
	for _, v := range []identity.Caller{a, b, c} {
		if e := reg.Add(v); e != nil {
			t.Fatal(e)
		}
	}
	o, e := invoke(m, r, p, a, "pty.open", Object{"cols": 80, "rows": 24})
	if e != nil {
		t.Fatal(e)
	}
	id := o["session_id"].(string)
	m.Orphan(a.ID)
	if _, e = invoke(m, r, p, b, "pty.transfer", Object{"session_id": id, "to_caller": b.ID}); e == nil {
		t.Fatal("cross-client orphan accepted")
	}
	if _, e = invoke(m, r, p, c, "pty.transfer", Object{"session_id": id, "to_caller": c.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = invoke(m, r, p, c, "pty.transfer", Object{"session_id": id, "to_caller": b.ID}); e != nil {
		t.Fatal("active voluntary transfer", e)
	}
	if got := fmt.Sprint(m.List()[0]["owner"]); got != b.ID {
		t.Fatal(got)
	}
}
