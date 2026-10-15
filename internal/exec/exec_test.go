//go:build !windows

package exec

import (
	"context"
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Manager, *cwd.Resolver, *policy.Engine) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	r, _ := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
	p, _ := policy.New(config.Policy{})
	m, e := New(config.Exec{MaxConcurrent: 2, ExclusiveSlots: 1, DefaultTimeout: "5s", IdempotencyCache: 10, InlineOutputBytes: "8B"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	return m, r, p
}
func run(m *Manager, r *cwd.Resolver, p *policy.Engine, caller string, a Object) (Object, error) {
	b, _ := json.Marshal(a)
	return m.Run(context.Background(), caller, r, p, b)
}
func Test_CTREXEC01_ArgvAndExit(t *testing.T) {
	m, r, p := fixture(t)
	o, e := run(m, r, p, "n/a", Object{"cmd": "/usr/bin/printf", "args": []string{"%s", "a; echo WRONG"}})
	if e != nil || o["stdout"] != "a; echo " || o["truncated"] != true {
		t.Fatal(o, e)
	}
	o, e = run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "printf no; exit 7"}})
	if e != nil || o["exit_code"] != 7 {
		t.Fatal(o, e)
	}
	_, e = run(m, r, p, "n/a", Object{"cmd": "xmesh-command-that-does-not-exist"})
	if e == nil || e.(*toolerr.Error).Code != "invalid_args" {
		t.Fatal(e)
	}
}
func Test_CONCONC09_IdempotencyAndIsolation(t *testing.T) {
	m, r, p := fixture(t)
	args := Object{"cmd": "/bin/sh", "args": []string{"-c", "printf x >> count; printf result; exit 4"}, "idempotency_key": "once"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := run(m, r, p, "n/a", args)
			if e != nil || o["exit_code"] != 4 {
				t.Errorf("%v %v", o, e)
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(r.Default, "count"))
	if string(b) != "x" {
		t.Fatalf("executions=%q", b)
	}
	if _, e := run(m, r, p, "n/b", args); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(filepath.Join(r.Default, "count"))
	if string(b) != "xx" {
		t.Fatalf("isolation %q", b)
	}
	cfg := config.Policy{}
	cfg.Exec.DenyPatterns = []string{"printf"}
	deny, _ := policy.New(cfg)
	if _, e := run(m, r, deny, "n/a", args); e == nil || e.(*toolerr.Error).Code != "policy_denied" {
		t.Fatal("cached policy bypass", e)
	}
}
func Test_CTREXEC02_OutputPagingAndTimeout(t *testing.T) {
	m, r, p := fixture(t)
	o, e := run(m, r, p, "n/a", Object{"cmd": "/usr/bin/printf", "args": []string{"0123456789abcdefgh"}})
	if e != nil {
		t.Fatal(e)
	}
	ref := o["output_ref"].(string)
	raw, _ := json.Marshal(Object{"output_ref": ref, "offset": 8, "limit": 6, "stream": "stdout"})
	page, e := m.Output("n/a", raw)
	if e != nil || page["stdout"] != "89abcd" || page["eof"] != false {
		t.Fatal(page, e)
	}
	if _, e = m.Output("n/b", raw); e == nil || e.(*toolerr.Error).Code != "not_found" {
		t.Fatal("output leaked", e)
	}
	_, e = run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "sleep 1; printf escaped > marker"}, "timeout": 30})
	if e == nil || e.(*toolerr.Error).Code != "timeout" {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = os.Stat(filepath.Join(r.Default, "marker")); !os.IsNotExist(e) {
		t.Fatal("timed out child survived")
	}
}
func Test_CONCONC10_ExclusiveBarrier(t *testing.T) {
	s := newScheduler(2, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	release, _, e := s.acquire(ctx, "a", "shared")
	if e != nil {
		t.Fatal(e)
	}
	events := make(chan string, 3)
	unblock := make(chan struct{})
	go func() {
		release, _, e := s.acquire(ctx, "b", "exclusive")
		if e != nil {
			events <- "error"
			return
		}
		events <- "exclusive"
		<-unblock
		release()
	}()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		n := len(s.queue)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exclusive not queued")
		}
		time.Sleep(time.Millisecond)
	}
	go func() {
		release, _, e := s.acquire(ctx, "c", "shared")
		if e != nil {
			events <- "error"
			return
		}
		events <- "shared"
		release()
	}()
	select {
	case v := <-events:
		t.Fatal("barrier crossed", v)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if v := <-events; v != "exclusive" {
		t.Fatal(v)
	}
	close(unblock)
	if v := <-events; v != "shared" {
		t.Fatal(v)
	}
}
func Test_RUL01_Saturation(t *testing.T) {
	s := newScheduler(1, 1)
	release, _, _ := s.acquire(context.Background(), "a", "shared")
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, e := s.acquire(ctx, "b", "shared")
	if e == nil || e.(*toolerr.Error).Code != "exec_saturated" || e.(*toolerr.Error).Fields["retry_after_ms"] == nil {
		t.Fatal(e)
	}
}
func Test_EnvironmentIsMinimal(t *testing.T) {
	m, r, p := fixture(t)
	t.Setenv("XMESH_INTERNAL_TEST", "private")
	o, e := run(m, r, p, "n/a", Object{"cmd": "/usr/bin/env"})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(o["stdout"].(string), "private") {
		t.Fatal("environment leaked")
	}
}
