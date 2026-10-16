// Package exec owns command concurrency, caller-scoped caching and output pages.
package exec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Object = map[string]any
type Args struct {
	Cmd     string            `json:"cmd"`
	Args    []string          `json:"args"`
	CWD     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
	Stdin   string            `json:"stdin"`
	Timeout json.RawMessage   `json:"timeout"`
	Mode    string            `json:"mode"`
	Key     string            `json:"idempotency_key"`
}
type record struct {
	key, ref, caller, stdout, stderr string
	result                           Object
	err                              error
	done                             chan struct{}
}
type Manager struct {
	mu        sync.Mutex
	cfg       config.Exec
	scheduler *scheduler
	cache     map[string]map[string]*record
	history   map[string][]*record
	outputs   map[string]*record
	dir       string
	wg        sync.WaitGroup
	closing   bool
	runs      map[uint64]*Run // in-flight runs, for the local control channel
	nextRun   uint64
}

// Run is the metadata of one queued or running call: who, which executable and
// how it is scheduled. It never holds arguments, environment, stdin or output.
type Run struct {
	ID      uint64
	Caller  string
	Cmd     string // base name only
	Mode    string
	Running bool
	Started time.Time
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)

func New(cfg config.Exec) (*Manager, error) {
	dir, e := os.MkdirTemp("", "xmesh-output-*")
	if e != nil {
		return nil, e
	}
	return &Manager{cfg: cfg, scheduler: newScheduler(cfg.MaxConcurrent, cfg.ExclusiveSlots), cache: map[string]map[string]*record{}, history: map[string][]*record{}, outputs: map[string]*record{}, runs: map[uint64]*Run{}, dir: dir}, nil
}
func (m *Manager) Configure(cfg config.Exec) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	m.scheduler.configure(cfg.MaxConcurrent, cfg.ExclusiveSlots)
}
func (m *Manager) Run(ctx context.Context, caller string, r *cwd.Resolver, p *policy.Engine, raw json.RawMessage) (Object, error) {
	a := Args{Mode: "shared"}
	if e := json.Unmarshal(raw, &a); e != nil {
		return nil, toolerr.Args("arguments")
	}
	if a.Mode != "shared" && a.Mode != "exclusive" {
		return nil, toolerr.Args("mode")
	}
	if a.Key != "" && !keyPattern.MatchString(a.Key) {
		return nil, toolerr.Args("idempotency_key")
	}
	if a.Cmd == "" || strings.ContainsRune(a.Cmd, 0) {
		return nil, toolerr.Args("cmd")
	}
	resolved, e := r.Directory(a.CWD)
	if e != nil {
		return nil, e
	}
	env, e := p.Environment(a.Env)
	if e != nil {
		return nil, e
	}
	if e = p.Command(a.Cmd, a.Args); e != nil {
		return nil, e
	}
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	timeout := config.Duration(cfg.DefaultTimeout)
	if len(a.Timeout) > 0 {
		var text string
		if json.Unmarshal(a.Timeout, &text) == nil {
			var err error
			timeout, err = time.ParseDuration(text)
			if err != nil {
				return nil, toolerr.Args("timeout")
			}
		} else {
			var ms int64
			if json.Unmarshal(a.Timeout, &ms) != nil || ms > int64((1<<63-1)/int64(time.Millisecond)) {
				return nil, toolerr.Args("timeout")
			}
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	if timeout <= 0 {
		return nil, toolerr.Args("timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, toolerr.New("internal", "执行管理器正在关闭", "op", "exec.run")
	}
	if m.cache[caller] == nil {
		m.cache[caller] = map[string]*record{}
	}
	if a.Key != "" {
		if old := m.cache[caller][a.Key]; old != nil {
			m.mu.Unlock()
			select {
			case <-old.done:
				return clone(old.result), old.err
			case <-ctx.Done():
				return nil, toolerr.New("timeout", "等待幂等执行结果超时", "timeout_ms", timeout.Milliseconds())
			}
		}
	}
	rec := &record{key: a.Key, caller: caller, done: make(chan struct{})}
	if a.Key != "" {
		m.cache[caller][a.Key] = rec
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	run := m.track(caller, a)
	defer m.untrack(run)
	result, err := m.execute(ctx, caller, resolved.Path, env, a, cfg, rec, timeout, p, run)
	m.mu.Lock()
	rec.result = clone(result)
	rec.err = err
	if err == nil || rec.ref != "" {
		m.history[caller] = append(m.history[caller], rec)
		for len(m.history[caller]) > cfg.IdempotencyCache {
			old := m.history[caller][0]
			m.history[caller] = m.history[caller][1:]
			m.evictLocked(old)
		}
	} else if a.Key != "" {
		delete(m.cache[caller], a.Key)
	}
	close(rec.done)
	m.mu.Unlock()
	return result, err
}
func (m *Manager) execute(ctx context.Context, caller, cwd string, env []string, a Args, cfg config.Exec, rec *record, timeout time.Duration, p *policy.Engine, run *Run) (Object, error) {
	release, wait, e := m.scheduler.acquire(ctx, caller, a.Mode)
	if e != nil {
		return nil, e
	}
	defer release()
	m.mu.Lock()
	run.Running = true
	m.mu.Unlock()
	if ctx.Err() != nil {
		return nil, toolerr.New("exec_saturated", "执行槽等待超时", "queue_position", 1, "mode", a.Mode, "retry_after_ms", 1000)
	}
	out, e := os.CreateTemp(m.dir, "stdout-*")
	if e != nil {
		return nil, toolerr.Internal("exec.output", e)
	}
	defer out.Close()
	errout, e := os.CreateTemp(m.dir, "stderr-*")
	if e != nil {
		os.Remove(out.Name())
		return nil, toolerr.Internal("exec.output", e)
	}
	defer errout.Close()
	keep := false
	defer func() {
		if !keep {
			os.Remove(out.Name())
			os.Remove(errout.Name())
		}
	}()
	started := time.Now()
	var code int
	var timedOut bool
	if runner := p.SandboxRunner(); runner != nil {
		code, timedOut, e = runManaged(ctx, runner, cwd, a, env, out, errout)
	} else {
		code, timedOut, e = runLaunched(ctx, p, cwd, a, env, out, errout)
	}
	if e != nil {
		return nil, e
	}
	duration := time.Since(started).Milliseconds()
	limit, _ := config.Size(cfg.InlineOutputBytes)
	stdout, outSize, readErr := inline(out, limit)
	if readErr != nil {
		return nil, toolerr.Internal("exec.output", readErr)
	}
	stderr, errSize, readErr := inline(errout, limit)
	if readErr != nil {
		return nil, toolerr.Internal("exec.output", readErr)
	}
	truncated := outSize > int64(limit) || errSize > int64(limit)
	result := Object{"exit_code": code, "stdout": stdout, "stderr": stderr, "duration_ms": duration, "truncated": truncated, "resolved_path": cwd}
	if wait >= time.Millisecond {
		result["queue_wait_ms"] = wait.Milliseconds()
	}
	if truncated || timedOut {
		random := make([]byte, 16)
		if _, e = rand.Read(random); e != nil {
			return nil, toolerr.Internal("exec.output_ref", e)
		}
		rec.ref = "o-" + hex.EncodeToString(random)
		rec.stdout = out.Name()
		rec.stderr = errout.Name()
		m.mu.Lock()
		m.outputs[rec.ref] = rec
		m.mu.Unlock()
		keep = true
		result["output_ref"] = rec.ref
	}
	if timedOut {
		return nil, toolerr.New("timeout", "命令执行超时，进程组已终止", "timeout_ms", timeout.Milliseconds(), "duration_ms", duration, "output_ref", rec.ref)
	}
	return result, nil
}
func clone(in Object) Object {
	if in == nil {
		return nil
	}
	out := Object{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func inline(f *os.File, n int) (string, int64, error) {
	st, e := f.Stat()
	if e != nil {
		return "", 0, e
	}
	b := make([]byte, min(int64(n), st.Size()))
	_, e = f.ReadAt(b, 0)
	if e == io.EOF {
		e = nil
	}
	return strings.ToValidUTF8(string(b), "�"), st.Size(), e
}
func (m *Manager) Output(caller string, raw json.RawMessage) (Object, error) {
	a := struct {
		Ref    string `json:"output_ref"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
		Stream string `json:"stream"`
	}{Limit: 65536, Stream: "both"}
	if e := json.Unmarshal(raw, &a); e != nil {
		return nil, toolerr.Args("arguments")
	}
	if a.Offset < 0 || a.Limit < 1 || (a.Stream != "both" && a.Stream != "stdout" && a.Stream != "stderr") {
		return nil, toolerr.Args("offset/limit/stream")
	}
	a.Limit = min(a.Limit, 1<<20)
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.outputs[a.Ref]
	if rec == nil || rec.caller != caller {
		return nil, toolerr.New("not_found", "输出引用不存在", "name", a.Ref)
	}
	out := Object{}
	maxRead := 0
	eof := true
	for name, path := range map[string]string{"stdout": rec.stdout, "stderr": rec.stderr} {
		if a.Stream != "both" && a.Stream != name {
			continue
		}
		f, e := os.Open(path)
		if e != nil {
			return nil, toolerr.New("not_found", "输出引用已释放", "name", a.Ref)
		}
		st, e := f.Stat()
		if e != nil {
			f.Close()
			return nil, e
		}
		b := make([]byte, a.Limit)
		n, e := f.ReadAt(b, a.Offset)
		f.Close()
		if e != nil && e != io.EOF {
			return nil, e
		}
		out[name] = strings.ToValidUTF8(string(b[:n]), "�")
		maxRead = max(maxRead, n)
		eof = eof && a.Offset+int64(n) >= st.Size()
	}
	out["next_offset"] = a.Offset + int64(maxRead)
	out["eof"] = eof
	return out, nil
}
func (m *Manager) evictLocked(rec *record) {
	if rec.key != "" && m.cache[rec.caller][rec.key] == rec {
		delete(m.cache[rec.caller], rec.key)
	}
	if rec.ref != "" {
		delete(m.outputs, rec.ref)
		_ = os.Remove(rec.stdout)
		_ = os.Remove(rec.stderr)
	}
}
func (m *Manager) track(caller string, a Args) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRun++
	run := &Run{ID: m.nextRun, Caller: caller, Cmd: filepath.Base(a.Cmd), Mode: a.Mode, Started: time.Now()}
	m.runs[run.ID] = run
	return run
}
func (m *Manager) untrack(run *Run) {
	m.mu.Lock()
	delete(m.runs, run.ID)
	m.mu.Unlock()
}

// Runs lists the calls that are queued or running, oldest first.
func (m *Manager) Runs() []Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Run, 0, len(m.runs))
	for _, r := range m.runs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *Manager) Counts() (shared, exclusive int) {
	m.scheduler.mu.Lock()
	defer m.scheduler.mu.Unlock()
	return m.scheduler.shared, m.scheduler.exclusive
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	m.wg.Wait()
	_ = os.RemoveAll(filepath.Clean(m.dir))
}
