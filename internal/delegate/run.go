package delegate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	runexec "github.com/tzf1003/xmesh/internal/exec"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// keyPattern is the idempotency key rule of CTR-EXEC-01, which CTR-DLG-02 shares.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)

var errNotStarted = errors.New("delegate: the runner process did not start")

// run implements delegate.run (CTR-DLG-02). The checks run in the contract's order: arguments, working directory,
// runner, command policy, idempotency, concurrency, then the start.
func (m *Manager) run(ctx context.Context, r Request, raw json.RawMessage) (Object, error) {
	var a struct {
		Runner    string          `json:"runner"`
		Prompt    string          `json:"prompt"`
		CWD       string          `json:"cwd"`
		Mode      string          `json:"mode"`
		SessionID *string         `json:"session_id"`
		Timeout   json.RawMessage `json:"timeout"`
		WaitMS    int64           `json:"wait_ms"`
		Key       string          `json:"idempotency_key"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return nil, toolerr.Args("arguments")
	}
	if a.Runner == "" {
		return nil, toolerr.Args("runner")
	}
	if a.Prompt == "" || len(a.Prompt) > maxPromptSize || !utf8.ValidString(a.Prompt) {
		return nil, toolerr.Args("prompt")
	}
	mode := a.Mode
	if mode == "" {
		mode = "read"
	}
	switch mode {
	case "read":
	case "write":
		if !r.Settings.AllowWrite {
			return nil, toolerr.New("policy_denied", "委托写模式未开启", "rule", "delegate.write_disabled")
		}
	default:
		return nil, toolerr.Args("mode")
	}
	sessionID := ""
	if a.SessionID != nil {
		if *a.SessionID == "" {
			return nil, toolerr.Args("session_id")
		}
		sessionID = *a.SessionID
	}
	if a.Key != "" && !keyPattern.MatchString(a.Key) {
		return nil, toolerr.Args("idempotency_key")
	}
	timeout, err := parseTimeout(a.Timeout, r.Settings.DefaultTimeoutDuration(), r.Settings.MaxTimeoutDuration())
	if err != nil {
		return nil, err
	}
	wait, err := waitDuration(a.WaitMS)
	if err != nil {
		return nil, err
	}

	resolved, err := r.Resolver.Directory(a.CWD)
	if err != nil {
		return nil, err
	}
	spec, ok := runnerFor(r.Settings, a.Runner)
	if !ok {
		return nil, toolerr.Args("runner")
	}
	if sessionID != "" && len(spec.resumeArgs) == 0 {
		return nil, toolerr.Args("session_id")
	}
	if _, err := runexec.LookupCommand(spec.cmd, resolved.Path); err != nil {
		return nil, toolerr.Args("runner")
	}
	argv := spec.argv(mode, sessionID)
	if err := r.Policy.Command(argv[0], argv[1:]); err != nil {
		return nil, err
	}
	env, err := r.Policy.Environment(nil)
	if err != nil {
		return nil, err
	}
	env = append(env, "XMESH_DELEGATE=1")
	fp := fingerprint(a.Runner, a.Prompt, resolved.Path, mode, sessionID, timeout.String())

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, toolerr.New("internal", "委托管理器正在关闭", "op", "delegate.run")
	}
	m.pruneLocked(time.Now())
	if a.Key != "" {
		if old := m.keys[r.Caller][a.Key]; old != nil {
			same := old.fingerprint == fp
			m.mu.Unlock()
			if !same {
				return nil, toolerr.Args("idempotency_key")
			}
			return m.await(ctx, old, wait)
		}
	}
	if sessionID != "" && !m.knownSessionLocked(r.Caller, a.Runner, sessionID) {
		m.mu.Unlock()
		return nil, toolerr.Args("session_id")
	}
	if limit := r.Settings.Concurrency(); m.running >= limit {
		in := m.running
		m.mu.Unlock()
		return nil, toolerr.New("delegate_saturated", "委托任务数已达上限", "in_use", in, "limit", limit)
	}
	procCtx, cancel := context.WithTimeout(m.base, timeout)
	t := &task{
		id:          newID("t-"),
		caller:      r.Caller,
		key:         a.Key,
		runner:      a.Runner,
		kind:        spec.kind,
		cwd:         resolved.Path,
		mode:        mode,
		fingerprint: fp,
		timeout:     timeout,
		retain:      r.Settings.RetainDuration(),
		started:     time.Now(),
		ready:       make(chan struct{}),
		changed:     make(chan struct{}),
		done:        make(chan struct{}),
		cancelFn:    cancel,
	}
	m.tasks[t.id] = t
	if a.Key != "" {
		if m.keys[r.Caller] == nil {
			m.keys[r.Caller] = map[string]*task{}
		}
		m.keys[r.Caller][a.Key] = t
	}
	m.running++
	m.wg.Add(1)
	m.mu.Unlock()

	m.launch(procCtx, r.Policy, t, argv, env, a.Prompt)
	return m.await(ctx, t, wait)
}

// await waits until the start of t is decided, then up to wait for the task to end, and replies with the task.
func (m *Manager) await(ctx context.Context, t *task, wait time.Duration) (Object, error) {
	<-t.ready
	if t.startErr != nil {
		return nil, toolerr.Internal("delegate.run", t.startErr)
	}
	waitFor(ctx, t.done, wait)
	m.mu.Lock()
	out := t.runViewLocked()
	m.mu.Unlock()
	return out, nil
}

// launch starts the runner process of t, with its standard input set to prompt, and reads both output streams
// until the process and the pipes are done. It returns once the goroutines run; the start is reported on t.ready.
func (m *Manager) launch(ctx context.Context, p *policy.Engine, t *task, argv, env []string, prompt string) {
	outR, outW, err := os.Pipe()
	if err != nil {
		m.fail(t, err)
		return
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeFiles(outR, outW)
		m.fail(t, err)
		return
	}
	var readers sync.WaitGroup
	for _, f := range []*os.File{outR, errR} {
		readers.Add(1)
		go func() {
			defer readers.Done()
			scanLines(f, maxLineBytes, func(line []byte) { m.consume(t, line) })
		}()
	}
	readersDone := make(chan struct{})
	go func() {
		readers.Wait()
		close(readersDone)
	}()
	go func() {
		defer m.wg.Done()
		started := false
		code, timedOut, runErr := runexec.RunStream(ctx, p, t.cwd, env, argv, prompt, outW, errW, func() {
			started = true
			t.setReady(nil)
		})
		if !started {
			if runErr == nil {
				runErr = errNotStarted
			}
			m.discard(t)
			t.setReady(runErr)
		}
		// The parent's copies of the write ends are closed here, so a reader sees end of file once the
		// process and every process holding the pipe are gone.
		closeFiles(outW, errW)
		// A child that outlived the command may keep a pipe open. The task does not wait for it beyond drainGrace.
		select {
		case <-readersDone:
		case <-time.After(drainGrace):
		}
		closeFiles(outR, errR)
		if started {
			m.finish(t, code, timedOut, runErr)
		}
	}()
}

// fail ends a task whose pipes could not be made: it is discarded, and its start is reported as failed.
func (m *Manager) fail(t *task, err error) {
	m.discard(t)
	t.setReady(err)
	m.wg.Done()
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// consume maps one line of runner output into t (CTR-DLG-05). Lines that arrive after the task has ended are ignored.
func (m *Manager) consume(t *task, line []byte) {
	p := parseLine(t.kind, line)
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.finished {
		return
	}
	if p.session != "" && t.sessionID == "" {
		t.sessionID = p.session
	}
	if p.hasAnswer {
		t.answer, t.hasAnswer = p.answer, true
	}
	if p.failed {
		t.failed = true
	}
	kept := false
	for _, e := range p.events {
		if e.typ == "assistant" {
			t.keepAssistantLocked(e.text)
		}
		if t.appendLocked(e.typ, e.text) {
			kept = true
		}
	}
	if kept {
		t.notifyLocked()
	}
}

// knownSessionLocked reports whether caller has a task on runner that returned session id, so that only sessions
// the caller has already used can be resumed. The caller holds m.mu.
func (m *Manager) knownSessionLocked(caller, runner, id string) bool {
	for _, t := range m.tasks {
		if t.caller == caller && t.runner == runner && t.sessionID == id {
			return true
		}
	}
	return false
}

// parseTimeout reads the timeout argument, a Go duration string or a whole number of milliseconds. A missing one is
// def, and a longer one is capped at limit (CTR-DLG-02).
func parseTimeout(raw json.RawMessage, def, limit time.Duration) (time.Duration, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return def, nil
	}
	var d time.Duration
	var text string
	var ms int64
	switch {
	case json.Unmarshal(raw, &text) == nil:
		v, err := time.ParseDuration(text)
		if err != nil {
			return 0, toolerr.Args("timeout")
		}
		d = v
	case json.Unmarshal(raw, &ms) == nil && ms <= math.MaxInt64/int64(time.Millisecond):
		d = time.Duration(ms) * time.Millisecond
	default:
		return 0, toolerr.Args("timeout")
	}
	if d <= 0 {
		return 0, toolerr.Args("timeout")
	}
	return min(d, limit), nil
}

// fingerprint identifies the arguments that an idempotency key stands for. Each part carries its length, so two
// different argument lists never produce the same input.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s;", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
