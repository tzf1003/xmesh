//go:build !windows

package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func newManager(t *testing.T, cfg config.Exec) (*Manager, *cwd.Resolver, *policy.Engine) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, r, p
}

func executions(t *testing.T, dir, name string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// Each caller keeps its most recent `idempotency_cache` entries: once the
// limit is exceeded the oldest entry is dropped, so reusing its key runs the
// command again, while newer keys still hit the cache. Another caller's cache
// is unaffected by this caller's churn, and a hit never runs the command again
// even when the first run exited non-zero.
// conformance: CON-CONC-09
func Test_CONCONC09_CacheKeepsMostRecentEntriesPerCaller(t *testing.T) {
	m, r, p := newManager(t, config.Exec{MaxConcurrent: 2, ExclusiveSlots: 1, DefaultTimeout: "5s", IdempotencyCache: 3, InlineOutputBytes: "256KiB"})
	dir := r.Default
	run1 := func(caller, key string) Object {
		t.Helper()
		o, err := run(m, r, p, caller, Object{"cmd": "/bin/sh", "args": []string{"-c", "printf x >> count-" + caller[len(caller)-1:] + "-" + key + "; exit 3"}, "idempotency_key": key})
		if err != nil || o["exit_code"] != 3 {
			t.Fatalf("%s/%s: %v %v", caller, key, o, err)
		}
		return o
	}
	count := func(caller, key string) int {
		return executions(t, dir, "count-"+caller[len(caller)-1:]+"-"+key)
	}

	run1("n/b", "theirs") // another caller's entry, older than everything below
	for _, key := range []string{"k1", "k2", "k3"} {
		run1("n/a", key)
	}
	for _, key := range []string{"k1", "k2", "k3"} {
		run1("n/a", key) // all three still cached: a non-zero exit is replayed, not re-run
		if count("n/a", key) != 1 {
			t.Fatalf("%s ran %d times, want one execution for the cached entry", key, count("n/a", key))
		}
	}
	run1("n/a", "k4") // the fourth entry pushes the oldest (k1) out
	run1("n/a", "k3")
	run1("n/a", "k4")
	if count("n/a", "k3") != 1 || count("n/a", "k4") != 1 {
		t.Fatalf("recent entries were not served from the cache: k3=%d k4=%d", count("n/a", "k3"), count("n/a", "k4"))
	}
	run1("n/a", "k1") // evicted: runs again (and in turn evicts k2)
	if count("n/a", "k1") != 2 {
		t.Fatalf("an evicted key must run again: executions = %d", count("n/a", "k1"))
	}
	run1("n/a", "k2")
	if count("n/a", "k2") != 2 {
		t.Fatalf("k2 was the oldest entry after k1 returned and must have been dropped: executions = %d", count("n/a", "k2"))
	}
	// The other caller's single entry survived all of that.
	run1("n/b", "theirs")
	if count("n/b", "theirs") != 1 {
		t.Fatalf("one caller's entries evicted another caller's: executions = %d", count("n/b", "theirs"))
	}
	// The same key from another caller is a separate entry.
	run1("n/b", "k3")
	if count("n/b", "k3") != 1 || count("n/a", "k3") != 1 {
		t.Fatalf("keys leaked between callers: a=%d b=%d", count("n/a", "k3"), count("n/b", "k3"))
	}
}

// The documented default for `idempotency_cache` is 200 entries per caller and
// is what a configuration without the setting gets.
// conformance: CON-CONC-09
func Test_CONCONC09_DefaultCacheSizeIs200(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "schemas", "examples", "xmesh.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Exec.IdempotencyCache != 200 {
		t.Fatalf("idempotency_cache default = %d, want 200", cfg.Agent.Exec.IdempotencyCache)
	}
	if cfg.Agent.Exec.InlineOutputBytes != "256KiB" {
		t.Fatalf("inline_output_bytes default = %q, want 256KiB", cfg.Agent.Exec.InlineOutputBytes)
	}
}

// An exclusive command that cannot get the shared slots drained within its own
// timeout returns exec_saturated and its process is never started; the shared
// command that was occupying the slot is not killed.
// conformance: CON-CONC-10
func Test_CONCONC10_ExclusiveTimesOutWithoutStarting(t *testing.T) {
	m, r, p := newManager(t, config.Exec{MaxConcurrent: 2, ExclusiveSlots: 1, DefaultTimeout: "20s", IdempotencyCache: 10, InlineOutputBytes: "256KiB"})
	dir := r.Default
	shared := make(chan Object, 1)
	sharedErr := make(chan error, 1)
	go func() {
		o, err := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "touch shared-started; while [ ! -f release ]; do sleep 0.02; done; printf alive"}, "timeout": "20s"})
		shared <- o
		sharedErr <- err
	}()
	waitFile(t, filepath.Join(dir, "shared-started"))

	started := time.Now()
	_, err := run(m, r, p, "n/b", Object{"cmd": "/bin/sh", "args": []string{"-c", "touch exclusive-started"}, "mode": "exclusive", "timeout": "300ms"})
	if e, ok := err.(*toolerr.Error); !ok || e.Code != "exec_saturated" {
		t.Fatalf("want exec_saturated, got %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("the exclusive call did not give up at its timeout: %v", time.Since(started))
	}
	// Even after the slot is free, the abandoned command must not appear.
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := <-shared
	if e := <-sharedErr; e != nil || o["exit_code"] != 0 || o["stdout"] != "alive" {
		t.Fatalf("the running shared command was disturbed: %v %v", o, e)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "exclusive-started")); !os.IsNotExist(err) {
		t.Fatal("the exclusive command was started although it reported exec_saturated")
	}
	if a, b := m.Counts(); a != 0 || b != 0 {
		t.Fatalf("slots leaked: shared=%d exclusive=%d", a, b)
	}
}

// An exclusive command starts only after the shared commands that were already
// running have finished (they are not killed), and shared commands arriving
// while it waits do not start ahead of it.
// conformance: CON-CONC-10
func Test_CONCONC10_ExclusiveWaitsForSharedToDrain(t *testing.T) {
	m, r, p := newManager(t, config.Exec{MaxConcurrent: 4, ExclusiveSlots: 1, DefaultTimeout: "20s", IdempotencyCache: 10, InlineOutputBytes: "256KiB"})
	dir := r.Default
	type result struct {
		o   Object
		err error
	}
	older := make(chan result, 1)
	go func() {
		o, err := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "touch older-started; while [ ! -f release ]; do sleep 0.02; done; touch older-done; printf older"}})
		older <- result{o, err}
	}()
	waitFile(t, filepath.Join(dir, "older-started"))

	exclusive := make(chan result, 1)
	go func() {
		o, err := run(m, r, p, "n/b", Object{"cmd": "/bin/sh", "args": []string{"-c", "test -f older-done && touch exclusive-ok; touch exclusive-ran"}, "mode": "exclusive"})
		exclusive <- result{o, err}
	}()
	waitQueued(t, m, 1)
	newer := make(chan result, 1)
	go func() {
		o, err := run(m, r, p, "n/c", Object{"cmd": "/bin/sh", "args": []string{"-c", "test -f exclusive-ran && touch newer-after-exclusive"}})
		newer <- result{o, err}
	}()
	waitQueued(t, m, 2)

	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "exclusive-ran")); !os.IsNotExist(err) {
		t.Fatal("the exclusive command started while a shared command was still running")
	}
	if _, err := os.Stat(filepath.Join(dir, "newer-after-exclusive")); !os.IsNotExist(err) {
		t.Fatal("a shared command started while an exclusive command was waiting")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]chan result{"older": older, "exclusive": exclusive, "newer": newer} {
		select {
		case res := <-ch:
			if res.err != nil || res.o["exit_code"] != 0 {
				t.Fatalf("%s: %v %v", name, res.o, res.err)
			}
			if name == "older" && res.o["stdout"] != "older" {
				t.Fatalf("the running shared command did not finish normally: %v", res.o)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s never finished", name)
		}
	}
	for _, name := range []string{"exclusive-ok", "newer-after-exclusive"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s is missing: ordering was not exclusive-after-drain, newer-after-exclusive", name)
		}
	}
}

// At most `exclusive_slots` exclusive commands run at once. Each command takes
// a lock directory and fails if somebody else already holds it; the same probe
// run in shared mode detects the overlap, which shows the probe works.
// conformance: CON-CONC-10
func Test_CONCONC10_ExclusiveSlotsBoundParallelism(t *testing.T) {
	probe := []string{"-c", "mkdir lock 2>/dev/null || exit 9; sleep 0.3; rmdir lock"}
	exits := func(mode string, slots int) []any {
		m, r, p := newManager(t, config.Exec{MaxConcurrent: 4, ExclusiveSlots: slots, DefaultTimeout: "30s", IdempotencyCache: 10, InlineOutputBytes: "256KiB"})
		var wg sync.WaitGroup
		var mu sync.Mutex
		var codes []any
		for i := 0; i < 3; i++ {
			wg.Add(1)
			// Stagger the starts so deadlines grow in queue order. With simultaneous starts the scheduler's
			// early saturation estimate (RUL-01) can refuse a waiter whose deadline was computed a few
			// microseconds before the deadline of the call it queues behind.
			time.Sleep(30 * time.Millisecond)
			go func() {
				defer wg.Done()
				o, err := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": probe, "mode": mode})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				codes = append(codes, o["exit_code"])
				mu.Unlock()
			}()
		}
		wg.Wait()
		return codes
	}
	overlapped := false
	for _, c := range exits("shared", 1) {
		overlapped = overlapped || c == 9
	}
	if !overlapped {
		t.Fatal("the probe did not detect overlapping shared commands")
	}
	for _, c := range exits("exclusive", 1) {
		if c != 0 {
			t.Fatalf("two exclusive commands ran at the same time with exclusive_slots=1: %v", c)
		}
	}
}

// Inline output: stdout and stderr are each returned in full up to
// `inline_output_bytes`. One byte more on either stream sets truncated, cuts
// that stream at the limit and returns an output_ref for the rest; the limit
// is a configuration value.
// conformance: RUL-05
func Test_RUL05_InlineLimitBoundary(t *testing.T) {
	const limit = 64
	m, r, p := newManager(t, config.Exec{MaxConcurrent: 2, ExclusiveSlots: 1, DefaultTimeout: "5s", IdempotencyCache: 10, InlineOutputBytes: "64B"})
	emit := func(out, errout int) Object {
		t.Helper()
		script := "head -c " + itoa(out) + " /dev/zero | tr '\\0' o; head -c " + itoa(errout) + " /dev/zero | tr '\\0' e >&2"
		o, err := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", script}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	exact := emit(limit, limit)
	if exact["truncated"] != false || exact["output_ref"] != nil || len(exact["stdout"].(string)) != limit || len(exact["stderr"].(string)) != limit {
		t.Fatalf("output of exactly the limit must be inline and complete: %v", exact)
	}
	for name, sizes := range map[string][2]int{"stdout over": {limit + 1, 3}, "stderr over": {3, limit + 1}, "both over": {limit + 10, limit + 10}} {
		o := emit(sizes[0], sizes[1])
		ref, _ := o["output_ref"].(string)
		if o["truncated"] != true || ref == "" {
			t.Fatalf("%s: %v", name, o)
		}
		if got := len(o["stdout"].(string)); got != min(sizes[0], limit) {
			t.Errorf("%s: stdout inline length %d", name, got)
		}
		if got := len(o["stderr"].(string)); got != min(sizes[1], limit) {
			t.Errorf("%s: stderr inline length %d", name, got)
		}
		// The overflow is reachable through exec.output.
		page, err := m.Output("n/a", mustJSON(Object{"output_ref": ref, "offset": limit, "limit": 1000, "stream": "stdout"}))
		if sizes[0] > limit && (err != nil || len(page["stdout"].(string)) != sizes[0]-limit) {
			t.Errorf("%s: paging the rest of stdout: %v %v", name, page, err)
		}
	}
	// The default is 256 KiB.
	if n, err := config.Size("256KiB"); err != nil || n != 256*1024 {
		t.Fatalf("256KiB = %d, %v", n, err)
	}
	big := newDefaultInline(t)
	if _, ok := big["stdout"].(string); !ok || len(big["stdout"].(string)) != 256*1024 || big["truncated"] != true {
		t.Fatalf("default inline limit: len=%d truncated=%v", len(big["stdout"].(string)), big["truncated"])
	}
}

func newDefaultInline(t *testing.T) Object {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "schemas", "examples", "xmesh.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	m, r, p := newManager(t, cfg.Agent.Exec)
	o, err := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "head -c " + itoa(256*1024+1) + " /dev/zero | tr '\\0' o"}})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func itoa(n int) string { return strconv.Itoa(n) }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// waitQueued blocks until n callers are waiting in the scheduler queue.
func waitQueued(t *testing.T, m *Manager, n int) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		m.scheduler.mu.Lock()
		have := len(m.scheduler.queue)
		m.scheduler.mu.Unlock()
		if have == n {
			return
		}
	}
	t.Fatalf("%d callers never queued", n)
}
