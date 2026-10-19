// Package pty owns device terminal sessions and their bounded scrollback.
package pty

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Signal(string) error
	Wait() error
	PID() int
}
type Session struct {
	ID, Owner, Node, OAuthClient, Budget, Name, CWD, State string
	Cols, Rows                                             int
	term                                                   terminal
	started                                                chan struct{}
	closedAt, orphanAt, created                            time.Time
	lastIO                                                 atomic.Int64
	opMu                                                   sync.Mutex // Orders owner operations and ownership changes.
	mu                                                     sync.Mutex // Session-local ring lock (CON-IMPL-03).
	ring                                                   []byte
	capacity                                               int
	cursor                                                 uint64
	eof                                                    bool
	changed                                                chan struct{}
}
type Manager struct {
	mu       sync.Mutex // Protects sessions, owners, quota reservations and config.
	sessions map[string]*Session
	byCaller map[string]int
	cfg      config.PTY
	callers  *identity.Registry
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	closing  bool
	epoch    string
}
type Args struct {
	SessionID string            `json:"session_id"`
	Shell     string            `json:"shell"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Cols      int               `json:"cols"`
	Rows      int               `json:"rows"`
	Name      string            `json:"name"`
	Data      string            `json:"data"`
	Cursor    uint64            `json:"cursor"`
	MaxBytes  int               `json:"max_bytes"`
	WaitMS    int               `json:"wait_ms"`
	Signal    string            `json:"signal"`
	ToCaller  string            `json:"to_caller"`
}
type Object = map[string]any

func New(cfg config.PTY, callers *identity.Registry) *Manager {
	m := &Manager{sessions: map[string]*Session{}, byCaller: map[string]int{}, cfg: cfg, callers: callers, stop: make(chan struct{}), done: make(chan struct{}), epoch: rand.Text()}
	go m.reaper()
	return m
}
func (m *Manager) Configure(cfg config.PTY) { m.mu.Lock(); m.cfg = cfg; m.mu.Unlock() }
func (m *Manager) Call(ctx context.Context, c identity.Caller, r *cwd.Resolver, p *policy.Engine, name string, raw json.RawMessage) (Object, error) {
	a := Args{MaxBytes: 32768, WaitMS: 1000}
	if e := json.Unmarshal(raw, &a); e != nil {
		return nil, toolerr.Args("arguments")
	}
	if name == "pty.open" {
		return m.open(c, r, p, a)
	}
	if name == "pty.list" {
		return Object{"sessions": m.List(), "caller_id": c.ID}, nil
	}
	m.mu.Lock()
	s := m.sessions[a.SessionID]
	if s == nil {
		m.mu.Unlock()
		return nil, toolerr.New("session_not_found", "终端会话不存在", "session_id", a.SessionID)
	}
	if name == "pty.read" {
		m.mu.Unlock()
		return s.read(ctx, a.Cursor, a.MaxBytes, time.Duration(a.WaitMS)*time.Millisecond)
	}
	m.mu.Unlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	m.mu.Lock()
	if m.sessions[a.SessionID] != s {
		m.mu.Unlock()
		return nil, toolerr.New("session_not_found", "终端会话不存在", "session_id", a.SessionID)
	}
	if name == "pty.transfer" {
		out, e := m.transferLocked(s, c, a.ToCaller)
		m.mu.Unlock()
		return out, e
	}
	if name == "pty.close" && s.State == "closed" {
		if c.ID != s.Owner {
			e := notOwner(s)
			m.mu.Unlock()
			return nil, e
		}
		m.mu.Unlock()
		return Object{"state": "closed"}, nil
	}
	if s.Owner != c.ID || s.State != "active" {
		e := notOwner(s)
		m.mu.Unlock()
		return nil, e
	}
	term := s.term
	m.mu.Unlock()
	if term == nil {
		return nil, toolerr.New("internal", "终端尚未就绪", "op", name)
	}
	switch name {
	case "pty.write":
		if len(a.Data) > 65536 {
			return nil, toolerr.Args("data")
		}
		n, e := term.Write([]byte(a.Data))
		if e != nil {
			return nil, toolerr.Internal(name, e)
		}
		s.touch()
		return Object{"written": n}, nil
	case "pty.resize":
		if !size(a.Cols, a.Rows) {
			return nil, toolerr.Args("cols/rows")
		}
		if e := term.Resize(a.Cols, a.Rows); e != nil {
			return nil, toolerr.Internal(name, e)
		}
		m.mu.Lock()
		s.Cols = a.Cols
		s.Rows = a.Rows
		m.mu.Unlock()
		s.touch()
		return Object{"cols": a.Cols, "rows": a.Rows}, nil
	case "pty.signal":
		if a.Signal != "INT" && a.Signal != "TERM" && a.Signal != "KILL" {
			return nil, toolerr.Args("signal")
		}
		if e := term.Signal(a.Signal); e != nil {
			return nil, toolerr.Internal(name, e)
		}
		s.touch()
		return Object{"sent": true}, nil
	case "pty.close":
		_ = term.Signal("TERM")
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		for {
			s.mu.Lock()
			eof := s.eof
			changed := s.changed
			s.mu.Unlock()
			if eof {
				return Object{"state": "closed"}, nil
			}
			select {
			case <-ctx.Done():
				_ = term.Signal("KILL")
				return nil, toolerr.New("timeout", "等待终端退出超时", "timeout_ms", 250)
			case <-timer.C:
				_ = term.Signal("KILL")
			case <-changed:
			}
		}
	default:
		return nil, toolerr.New("not_found", "工具不存在", "name", name)
	}
}
func notOwner(s *Session) error {
	return toolerr.New("not_owner", "此调用方不是终端所有者", "session_id", s.ID, "owner", s.Owner)
}
func size(c, r int) bool { return c >= 1 && c <= 512 && r >= 1 && r <= 512 }
func (m *Manager) quotaLocked(c identity.Caller) error {
	total := 0
	var oldest *Session
	for _, s := range m.sessions {
		if s.State == "active" || s.State == "orphaned" {
			total++
			if oldest == nil || s.lastIO.Load() < oldest.lastIO.Load() {
				oldest = s
			}
		}
	}
	scope := "device"
	inUse, limit := total, m.cfg.MaxSessions
	if inUse < limit {
		scope = "caller"
		inUse = m.byCaller[c.ID]
		limit = m.cfg.MaxPerCaller
	}
	if inUse < limit {
		return m.budgetLocked(identity.Budget(c), "")
	}
	old := Object{"id": "", "idle_s": 0}
	if oldest != nil {
		old["id"] = oldest.ID
		old["idle_s"] = int64(time.Since(time.Unix(0, oldest.lastIO.Load())).Seconds())
	}
	return toolerr.New("pty_limit", "终端会话配额已满", "scope", scope, "limit", limit, "in_use", inUse, "oldest_idle", old)
}
func (m *Manager) budgetLocked(budget, exclude string) error {
	inUse := 0
	for _, s := range m.sessions {
		if s.ID != exclude && s.Budget == budget && (s.State == "active" || s.State == "orphaned") {
			inUse++
		}
	}
	if inUse >= m.cfg.MaxPerCaller {
		return toolerr.New("policy_denied", "客户端终端配额已满", "rule", "quota.exceeded", "reason", "同一 OAuth client 或节点的终端数量已达到 max_per_caller")
	}
	return nil
}
func (m *Manager) open(c identity.Caller, r *cwd.Resolver, p *policy.Engine, a Args) (Object, error) {
	if !identity.Valid(c.ID) || strings.HasSuffix(c.ID, "/init") {
		return nil, toolerr.New("unauthenticated", "终端需要已建立的 MCP 会话")
	}
	if !size(a.Cols, a.Rows) || len([]rune(a.Name)) > 64 {
		return nil, toolerr.Args("cols/rows/name")
	}
	resolved, e := r.Directory(a.CWD)
	if e != nil {
		return nil, e
	}
	env, e := p.Environment(a.Env)
	if e != nil {
		return nil, e
	}
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	shell := a.Shell
	if shell == "" {
		shell = cfg.DefaultShell
	}
	auto := shell == "" || shell == "auto"
	shell, e = DefaultShell(shell)
	if e != nil {
		return nil, toolerr.Args("shell")
	}
	if e = unsupportedShell(shell); e != nil {
		return nil, e
	}
	if e = p.Command(shell, nil); e != nil {
		return nil, e
	}
	runner := p.SandboxRunner()
	var launch string
	var argv []string
	var innerTerminal bool
	baseEnv := env
	if runner == nil {
		launch, argv, env, innerTerminal, e = p.SandboxPTY(shell, nil, resolved.Path, baseEnv)
		if e != nil {
			return nil, e
		}
	}
	random := make([]byte, 12)
	if _, e = rand.Read(random); e != nil {
		return nil, e
	}
	capacity, _ := config.Size(cfg.ScrollbackBytes)
	s := &Session{ID: "p-" + hex.EncodeToString(random), Owner: c.ID, Node: identity.Node(c.ID), OAuthClient: c.OAuthClient, Budget: identity.Budget(c), Name: a.Name, CWD: resolved.Path, State: "active", Cols: a.Cols, Rows: a.Rows, created: time.Now(), capacity: capacity, changed: make(chan struct{}), started: make(chan struct{})}
	s.touch()
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, toolerr.New("internal", "终端管理器正在关闭", "op", "pty.open")
	}
	if e = m.quotaLocked(c); e != nil {
		m.mu.Unlock()
		return nil, e
	}
	m.sessions[s.ID] = s
	m.byCaller[c.ID]++
	m.mu.Unlock()
	var term terminal
	if runner != nil {
		term, e = startManaged(runner, shell, resolved.Path, env, a.Cols, a.Rows)
	} else {
		term, e = start(launch, argv, resolved.Path, env, a.Cols, a.Rows, innerTerminal)
		if e != nil && auto {
			// `auto` must not strand the caller on a first choice the system
			// cannot launch; an explicitly requested shell is never replaced.
			if alt, ok := alternateShell(shell); ok && p.Command(alt, nil) == nil {
				if l, av, ev, inner, pe := p.SandboxPTY(alt, nil, resolved.Path, baseEnv); pe == nil {
					if t2, e2 := start(l, av, resolved.Path, ev, a.Cols, a.Rows, inner); e2 == nil {
						term, e = t2, nil
					}
				}
			}
		}
	}
	m.mu.Lock()
	if e != nil {
		if s.State == "active" {
			m.byCaller[s.Owner]--
		}
		delete(m.sessions, s.ID)
		close(s.started)
		m.mu.Unlock()
		if runner != nil {
			return nil, toolerr.SandboxRunner("sandbox.runner_failed", e)
		}
		return nil, toolerr.Internal("pty.open", e)
	}
	s.term = term
	close(s.started)
	m.mu.Unlock()
	go m.pump(s)
	return Object{"session_id": s.ID, "pid": term.PID(), "resolved_path": resolved.Path, "owner": c.ID}, nil
}
func (s *Session) touch()  { s.lastIO.Store(time.Now().UnixNano()) }
func (s *Session) notify() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Session) append(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor += uint64(len(b))
	if len(b) >= s.capacity {
		s.ring = append(s.ring[:0], b[len(b)-s.capacity:]...)
	} else {
		over := len(s.ring) + len(b) - s.capacity
		if over > 0 {
			copy(s.ring, s.ring[over:])
			s.ring = s.ring[:len(s.ring)-over]
		}
		s.ring = append(s.ring, b...)
	}
	s.notify()
}
func (m *Manager) pump(s *Session) {
	b := make([]byte, 32768)
	for {
		n, e := s.term.Read(b)
		if n > 0 {
			s.append(b[:n])
		}
		if e != nil {
			break
		}
	}
	_ = s.term.Wait()
	_ = s.term.Close()
	m.mu.Lock()
	if s.State == "active" {
		m.byCaller[s.Owner]--
	}
	s.State = "closed"
	s.closedAt = time.Now()
	s.mu.Lock()
	s.eof = true
	s.notify()
	s.mu.Unlock()
	m.retainLocked(time.Now())
	m.mu.Unlock()
}
func (s *Session) read(ctx context.Context, cursor uint64, maxBytes int, wait time.Duration) (Object, error) {
	// Over-large bounds clamp instead of failing, matching exec.output's
	// limit handling (CTR-PTY-02). Negative values stay invalid.
	if maxBytes < 0 || wait < 0 {
		return nil, toolerr.Args("max_bytes/wait_ms")
	}
	if maxBytes == 0 {
		maxBytes = 32768
	}
	if maxBytes > 1<<20 {
		maxBytes = 1 << 20
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		start := s.cursor - uint64(len(s.ring))
		offset := cursor
		truncated := offset < start
		if truncated {
			offset = start
		}
		end := min(s.cursor, offset+uint64(maxBytes))
		if offset < s.cursor || s.eof || wait == 0 {
			data := ""
			next := offset
			if offset < s.cursor {
				raw := s.ring[offset-start : end-start]
				complete := 0
				for complete < len(raw) {
					if !utf8.FullRune(raw[complete:]) {
						if end < s.cursor && complete == 0 {
							end = min(s.cursor, offset+utf8.UTFMax)
							raw = s.ring[offset-start : end-start]
							if utf8.FullRune(raw) {
								continue
							}
						}
						if end == s.cursor && s.eof {
							complete = len(raw)
						}
						break
					}
					_, size := utf8.DecodeRune(raw[complete:])
					complete += size
				}
				data = strings.ToValidUTF8(string(raw[:complete]), "�")
				next = offset + uint64(complete)
			}
			eof := s.eof && next >= s.cursor
			if data == "" && next == offset && !s.eof && wait > 0 {
				changed := s.changed
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return Object{"data": "", "next_cursor": offset, "eof": false, "truncated": truncated}, nil
				case <-changed:
					continue
				}
			}
			s.mu.Unlock()
			if data != "" {
				s.touch()
			}
			return Object{"data": data, "next_cursor": next, "eof": eof, "truncated": truncated}, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return Object{"data": "", "next_cursor": offset, "eof": false, "truncated": truncated}, nil
		case <-changed:
		}
	}
}
func (m *Manager) transferLocked(s *Session, c identity.Caller, to string) (Object, error) {
	target, exists := m.callers.Get(to)
	if !identity.Valid(to) || !exists {
		return nil, toolerr.Args("to_caller")
	}
	if s.State == "active" {
		if s.Owner != c.ID {
			return nil, notOwner(s)
		}
	} else if s.State == "orphaned" {
		if identity.Node(c.ID) != s.Node || identity.Node(to) != s.Node || (s.OAuthClient != "" && (c.OAuthClient != s.OAuthClient || target.OAuthClient != s.OAuthClient)) {
			return nil, notOwner(s)
		}
	} else {
		return nil, notOwner(s)
	}
	if s.Owner == to {
		return Object{"owner": to, "state": s.State}, nil
	}
	if m.byCaller[to] >= m.cfg.MaxPerCaller {
		return nil, toolerr.New("pty_limit", "接收方终端配额已满", "scope", "caller", "limit", m.cfg.MaxPerCaller, "in_use", m.byCaller[to], "oldest_idle", Object{"id": s.ID, "idle_s": 0})
	}
	if e := m.budgetLocked(identity.Budget(target), s.ID); e != nil {
		return nil, e
	}
	if s.State == "active" {
		m.byCaller[s.Owner]--
	}
	s.Owner = to
	s.Budget = identity.Budget(target)
	s.State = "active"
	m.byCaller[to]++
	s.touch()
	return Object{"owner": to, "state": s.State}, nil
}
func (m *Manager) Orphan(caller string) {
	m.mu.Lock()
	sessions := []*Session{}
	for _, s := range m.sessions {
		if s.Owner == caller && s.State == "active" {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.opMu.Lock()
		m.mu.Lock()
		if s.Owner == caller && s.State == "active" {
			m.byCaller[caller]--
			s.Owner = ""
			s.State = "orphaned"
			s.orphanAt = time.Now()
		}
		m.mu.Unlock()
		s.opMu.Unlock()
	}
}
func (m *Manager) List() []Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Object{}
	for _, s := range m.sessions {
		pid := 0
		if s.State != "closed" && s.term != nil {
			pid = s.term.PID()
		}
		out = append(out, Object{"id": s.ID, "name": s.Name, "owner": s.Owner, "pid": pid, "state": s.State, "idle_s": max(int64(0), int64(time.Since(time.Unix(0, s.lastIO.Load())).Seconds())), "cols": s.Cols, "rows": s.Rows, "cwd": s.CWD})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	return out
}

// View is the metadata of one session for the local control channel. It has no terminal
// content; pty.list stays the contract surface for tools.
type View struct {
	ID, Name, Owner, OAuthClient, State, CWD string
	PID, IdleS, Cols, Rows                   int
	Created                                  time.Time
}

func (m *Manager) Views() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []View{}
	for _, s := range m.sessions {
		pid := 0
		if s.State != "closed" && s.term != nil {
			pid = s.term.PID()
		}
		idle := max(int64(0), int64(time.Since(time.Unix(0, s.lastIO.Load())).Seconds()))
		out = append(out, View{ID: s.ID, Name: s.Name, Owner: s.Owner, OAuthClient: s.OAuthClient, State: s.State, CWD: s.CWD, PID: pid, IdleS: int(idle), Cols: s.Cols, Rows: s.Rows, Created: s.created})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *Manager) Counts() (inUse, active, closed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		switch s.State {
		case "active":
			active++
			inUse++
		case "orphaned":
			inUse++
		case "closed":
			closed++
		}
	}
	return
}
func (m *Manager) retainLocked(now time.Time) {
	closed := []*Session{}
	for _, s := range m.sessions {
		if s.State == "closed" {
			closed = append(closed, s)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].closedAt.Before(closed[j].closedAt) })
	for i, s := range closed {
		if now.Sub(s.closedAt) >= config.Duration(m.cfg.ClosedRetain) || i < len(closed)-m.cfg.ClosedRetainMax {
			s.State = "released"
			delete(m.sessions, s.ID)
		}
	}
}
func (m *Manager) Reap(now time.Time) {
	m.mu.Lock()
	sessions := []*Session{}
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.retainLocked(now)
	m.mu.Unlock()
	for _, s := range sessions {
		s.opMu.Lock()
		m.mu.Lock()
		term := s.term
		expired := term != nil && ((s.State == "active" && now.Sub(time.Unix(0, s.lastIO.Load())) >= config.Duration(m.cfg.IdleTimeout)) || (s.State == "orphaned" && now.Sub(s.orphanAt) >= config.Duration(m.cfg.OrphanGrace)))
		m.mu.Unlock()
		if expired {
			_ = term.Signal("KILL")
		}
		s.opMu.Unlock()
	}
}
func (m *Manager) reaper() {
	defer close(m.done)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			m.Reap(now)
		}
	}
}

// CloseAll kills every live session but leaves the manager usable, so a
// drain with kill can be followed by Resume and new pty.open calls. Shutdown is
// reserved for process exit.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := m.liveLocked()
	m.mu.Unlock()
	m.killSessions(sessions)
}

// Shutdown stops the reaper, refuses new sessions for good and kills the rest.
func (m *Manager) Shutdown() {
	m.once.Do(func() {
		close(m.stop)
		<-m.done
		m.mu.Lock()
		m.closing = true
		sessions := m.liveLocked()
		m.mu.Unlock()
		m.killSessions(sessions)
	})
}
func (m *Manager) liveLocked() []*Session {
	sessions := []*Session{}
	for _, s := range m.sessions {
		if s.State != "closed" {
			sessions = append(sessions, s)
		}
	}
	return sessions
}

// killSessions signals KILL to each session and waits until its output has
// reached EOF.
func (m *Manager) killSessions(sessions []*Session) {
	for _, s := range sessions {
		<-s.started
		m.mu.Lock()
		term := s.term
		m.mu.Unlock()
		if term != nil {
			_ = term.Signal("KILL")
		}
	}
	for _, s := range sessions {
		if s.term != nil {
			for {
				s.mu.Lock()
				eof := s.eof
				changed := s.changed
				s.mu.Unlock()
				if eof {
					break
				}
				<-changed
			}
		}
	}
}
func DefaultShell(shell string) (string, error) {
	if shell != "" && shell != "auto" {
		return exec.LookPath(shell)
	}
	return defaultShell()
}
