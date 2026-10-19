package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

type operation struct {
	digest   [32]byte
	result   ActionResult
	done     chan struct{}
	previous <-chan struct{}
}

type ownedSession struct {
	Session
	frames  map[string]Frame
	actions map[string]*operation
	ctx     context.Context
	cancel  context.CancelFunc
}

// Manager keeps authority on the target Agent, including across Gateway reconnects.
type Manager struct {
	mu         sync.Mutex
	provider   Provider
	settings   Settings
	paused     bool
	closed     bool
	serial     uint64
	tail       <-chan struct{}
	sessions   map[string]*ownedSession
	controller string
	queue      chan struct{}
	executing  chan struct{}
	stop       chan struct{}
	done       chan struct{}
	workers    sync.WaitGroup
	authorize  func(string) bool
	now        func() time.Time
}

func New(p Provider, settings Settings) *Manager {
	m := &Manager{provider: p, settings: settings.Effective(), sessions: map[string]*ownedSession{}, queue: make(chan struct{}, 8), executing: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), now: time.Now}
	ready := make(chan struct{})
	close(ready)
	m.tail = ready
	go m.sweep()
	return m
}

func (m *Manager) sweep() {
	defer close(m.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.mu.Lock()
			m.expireLocked()
			active := len(m.sessions) > 0 && m.settings.Enabled
			m.mu.Unlock()
			if active {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, _ = m.Info(ctx)
				cancel()
			}
		}
	}
}

func (m *Manager) Configure(settings Settings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings = settings.Effective()
	if m.settings.Enabled && !settings.Enabled {
		m.revokeLocked("")
	}
	m.settings = settings
}

func (m *Manager) availableLocked() error {
	if m.closed || !m.settings.Enabled {
		return toolerr.New("policy_denied", "桌面能力已关闭", "rule", "computer.disabled")
	}
	if m.provider == nil {
		return toolerr.New("desktop_unavailable", "用户桌面执行器未连接", "reason", "executor_disconnected")
	}
	return nil
}

func (m *Manager) Info(ctx context.Context) (Info, error) {
	m.mu.Lock()
	err := m.availableLocked()
	m.mu.Unlock()
	if err != nil {
		return Info{}, err
	}
	info, err := m.provider.Info(ctx)
	if err != nil {
		m.mu.Lock()
		m.revokeLocked("")
		m.mu.Unlock()
		return Info{}, err
	}
	if info.ProtocolVersion != ProtocolVersion {
		m.mu.Lock()
		m.revokeLocked("")
		m.mu.Unlock()
		return Info{}, toolerr.New("desktop_unavailable", "桌面执行器协议不兼容", "reason", "protocol_mismatch")
	}
	m.mu.Lock()
	for id, s := range m.sessions {
		if !info.Available || s.Epoch != info.Epoch || (s.Mode == "control" && !inputCapable(info)) {
			m.removeLocked(id)
		}
	}
	m.mu.Unlock()
	return info, nil
}

func desktopReady(info Info) error {
	if !info.Available {
		code := "desktop_unavailable"
		if info.State == "locked" {
			code = "desktop_locked"
		}
		if info.State == "permission_required" {
			code = "permission_required"
		}
		return toolerr.New(code, "桌面当前不可用", "reason", info.Reason, "state", info.State)
	}
	if info.Epoch == "" || info.DesktopID == "" || info.LayoutVersion == "" {
		return toolerr.New("desktop_unavailable", "执行器未提供桌面身份", "reason", "invalid_identity")
	}
	return nil
}

func inputCapable(info Info) bool {
	return slices.Contains(info.Capabilities, "pointer") && slices.Contains(info.Capabilities, "keyboard")
}

func (m *Manager) ttl(seconds int) (time.Duration, error) {
	if seconds == 0 {
		seconds = m.settings.LeaseSeconds
	}
	if seconds < 10 || seconds > 600 {
		return 0, toolerr.Args("ttl_seconds")
	}
	return time.Duration(seconds) * time.Second, nil
}

func (m *Manager) Open(ctx context.Context, caller string, r OpenRequest) (Session, error) {
	if r.Mode != "observe" && r.Mode != "control" {
		return Session{}, toolerr.Args("mode")
	}
	info, err := m.Info(ctx)
	if err != nil {
		return Session{}, err
	}
	if err = desktopReady(info); err != nil {
		return Session{}, err
	}
	if !slices.Contains(info.Capabilities, "screenshot") || (r.Mode == "control" && !inputCapable(info)) {
		return Session{}, toolerr.New("permission_required", "需要系统桌面权限", "reason", "desktop_capability_missing")
	}
	if r.DesktopID != "" && r.DesktopID != info.DesktopID {
		return Session{}, toolerr.New("not_found", "桌面不存在", "name", r.DesktopID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.availableLocked(); err != nil {
		return Session{}, err
	}
	m.expireLocked()
	for id, s := range m.sessions {
		if s.Epoch != info.Epoch {
			m.removeLocked(id)
		}
	}
	if r.Mode == "control" && m.paused {
		return Session{}, toolerr.New("policy_denied", "桌面控制已暂停", "rule", "computer.paused")
	}
	if r.Mode == "control" && m.controller != "" {
		s := m.sessions[m.controller]
		return Session{}, toolerr.New("resource_busy", "桌面已有控制者", "resource", "computer:"+info.DesktopID, "held_by", s.Owner, "reason", "held", "expires_in_s", max(0, int(s.ExpiresAt.Sub(m.now()).Seconds())))
	}
	if len(m.sessions) >= m.settings.MaxSessions {
		return Session{}, toolerr.New("resource_busy", "桌面会话达到上限", "resource", "computer:sessions", "limit", m.settings.MaxSessions)
	}
	ttl, err := m.ttl(r.TTLSeconds)
	if err != nil {
		return Session{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &ownedSession{Session: Session{ID: "cs-" + rand.Text(), DesktopID: info.DesktopID, Epoch: info.Epoch, Mode: r.Mode, Owner: caller, ExpiresAt: m.now().Add(ttl)}, frames: map[string]Frame{}, actions: map[string]*operation{}, ctx: ctx, cancel: cancel}
	if r.Mode == "control" {
		m.serial++
		s.Token = strconv.FormatUint(m.serial, 10)
		m.controller = s.ID
	}
	m.sessions[s.ID] = s
	return s.Session, nil
}

func (m *Manager) sessionLocked(caller string, r SessionRequest, control bool) (*ownedSession, error) {
	if err := m.availableLocked(); err != nil {
		return nil, err
	}
	m.expireLocked()
	s := m.sessions[r.SessionID]
	if s == nil {
		return nil, toolerr.New("session_not_found", "桌面会话不存在或已失效", "session_id", r.SessionID)
	}
	if s.Owner != caller {
		return nil, toolerr.New("not_owner", "桌面会话属于其他调用方", "session_id", s.ID, "owner", s.Owner)
	}
	if r.Epoch != s.Epoch {
		return nil, toolerr.New("conflict", "桌面会话代次已变化", "reason", "desktop_epoch")
	}
	if control {
		if m.authorize != nil && !m.authorize(caller) {
			m.removeLocked(s.ID)
			return nil, toolerr.New("policy_denied", "桌面控制授权已撤销", "rule", "computer.authorization_revoked")
		}
		if m.paused {
			return nil, toolerr.New("policy_denied", "桌面控制已暂停", "rule", "computer.paused")
		}
		if s.Mode != "control" || s.Token != r.Token || m.controller != s.ID {
			return nil, toolerr.New("lease_required", "需要当前桌面的控制租约")
		}
	}
	return s, nil
}

func (m *Manager) Renew(caller string, r RenewRequest) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.sessionLocked(caller, r.SessionRequest, false)
	if err != nil {
		return Session{}, err
	}
	if s.Mode == "control" && r.Token != s.Token {
		return Session{}, toolerr.New("lease_required", "控制租约已失效")
	}
	ttl, err := m.ttl(r.TTLSeconds)
	if err != nil {
		return Session{}, err
	}
	s.ExpiresAt = m.now().Add(ttl)
	return s.Session, nil
}

func (m *Manager) Observe(ctx context.Context, caller string, r ObserveRequest) (Frame, error) {
	if r.MaxWidth < 0 || r.MaxWidth > 8192 {
		return Frame{}, toolerr.Args("max_width")
	}
	if r.Region != nil && (r.Region.X < 0 || r.Region.Y < 0 || r.Region.Width < 1 || r.Region.Height < 1) {
		return Frame{}, toolerr.Args("region")
	}
	m.mu.Lock()
	s, err := m.sessionLocked(caller, r.SessionRequest, false)
	m.mu.Unlock()
	if err != nil {
		return Frame{}, err
	}
	info, err := m.Info(ctx)
	if err != nil {
		return Frame{}, err
	}
	if err = desktopReady(info); err != nil {
		m.invalidate(s.ID)
		return Frame{}, err
	}
	if info.Epoch != s.Epoch {
		m.invalidate(s.ID)
		return Frame{}, toolerr.New("conflict", "桌面会话已重建", "reason", "desktop_epoch")
	}
	f, err := m.provider.Capture(ctx, r.CaptureRequest)
	if err != nil {
		return Frame{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err = m.sessionLocked(caller, r.SessionRequest, false); err != nil {
		return Frame{}, err
	}
	if f.Epoch != s.Epoch || f.LayoutVersion != info.LayoutVersion {
		return Frame{}, toolerr.New("conflict", "采集期间桌面布局已变化", "reason", "layout_changed")
	}
	if len(f.Image) == 0 || len(f.Image) > m.settings.MaxImageBytes {
		return Frame{}, toolerr.New("too_large", "桌面图像大小无效", "size", len(f.Image), "limit", m.settings.MaxImageBytes)
	}
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(f.Image))
	if err != nil || (kind != "png" && kind != "jpeg") || cfg.Width != f.Width || cfg.Height != f.Height || f.Width < 1 || f.Height < 1 || f.Width > 8192 || f.Height > 8192 || f.Region.Width < 1 || f.Region.Height < 1 {
		return Frame{}, toolerr.New("desktop_unavailable", "执行器返回的截图无效", "reason", "invalid_frame")
	}
	f.ID = "f-" + rand.Text()
	f.CapturedAt = m.now()
	f.CoordinateSpace = "image_pixels"
	f.MIMEType = "image/" + kind
	if len(s.frames) >= 4 {
		oldest := ""
		var at time.Time
		for id, v := range s.frames {
			if oldest == "" || v.CapturedAt.Before(at) {
				oldest, at = id, v.CapturedAt
			}
		}
		delete(s.frames, oldest)
	}
	s.frames[f.ID] = f
	return f, nil
}

func validActions(actions []Action) error {
	if len(actions) < 1 || len(actions) > 8 {
		return toolerr.Args("actions")
	}
	budget := 0
	for _, a := range actions {
		if a.DurationMS < 0 || a.DurationMS > 2000 || a.Clicks < 0 || a.Clicks > 3 || len(a.Text) > 65536 || !utf8.ValidString(a.Text) || len(a.Keys) > 8 {
			return toolerr.Args("actions")
		}
		budget += a.DurationMS
		if a.Type != "text" && a.Text != "" {
			return toolerr.Args("actions.text")
		}
		if a.Type != "key" && len(a.Keys) > 0 {
			return toolerr.Args("actions.keys")
		}
		if a.Type != "move" && a.Type != "click" && a.Type != "drag" && a.Type != "scroll" && (a.X != nil || a.Y != nil) {
			return toolerr.Args("actions.coordinates")
		}
		if a.Type != "drag" && (a.ToX != nil || a.ToY != nil) {
			return toolerr.Args("actions.to_coordinates")
		}
		switch a.Type {
		case "move", "click", "drag", "scroll":
			if a.X == nil || a.Y == nil {
				return toolerr.Args("actions.coordinates")
			}
			if a.Type == "drag" && (a.ToX == nil || a.ToY == nil) {
				return toolerr.Args("actions.to_coordinates")
			}
			if a.Button != "" && a.Button != "left" && a.Button != "middle" && a.Button != "right" {
				return toolerr.Args("actions.button")
			}
			if a.DeltaX < -10000 || a.DeltaX > 10000 || a.DeltaY < -10000 || a.DeltaY > 10000 {
				return toolerr.Args("actions.scroll")
			}
		case "key":
			if len(a.Keys) == 0 {
				return toolerr.Args("actions.keys")
			}
			for _, k := range a.Keys {
				if len(k) == 0 || len(k) > 32 || strings.ContainsAny(k, "\x00\r\n") {
					return toolerr.Args("actions.keys")
				}
			}
		case "text":
			if a.Text == "" || strings.ContainsRune(a.Text, 0) {
				return toolerr.Args("actions.text")
			}
		case "wait":
			if a.DurationMS == 0 {
				return toolerr.Args("actions.duration_ms")
			}
		default:
			return toolerr.Args("actions.type")
		}
	}
	if budget > 4000 {
		return toolerr.Args("actions.duration_ms")
	}
	return nil
}

func (m *Manager) Act(ctx context.Context, caller string, r ActRequest) (ActionResult, error) {
	if len(r.ActionID) < 1 || len(r.ActionID) > 128 {
		return ActionResult{}, toolerr.Args("action_id")
	}
	if err := validActions(r.Actions); err != nil {
		return ActionResult{}, err
	}
	raw, _ := json.Marshal(r)
	digest := sha256.Sum256(raw)
	m.mu.Lock()
	s, err := m.sessionLocked(caller, r.SessionRequest, true)
	if err != nil {
		m.mu.Unlock()
		return ActionResult{}, err
	}
	if op := s.actions[r.ActionID]; op != nil {
		if op.digest != digest {
			m.mu.Unlock()
			return ActionResult{}, toolerr.New("conflict", "动作 ID 已用于其他参数", "reason", "action_id_reused")
		}
		m.mu.Unlock()
		return m.wait(ctx, s, op)
	}
	if len(s.actions) >= 128 {
		m.mu.Unlock()
		return ActionResult{}, toolerr.New("resource_busy", "动作缓存达到上限，请创建新会话", "resource", "computer:actions")
	}
	var frame *Frame
	for _, a := range r.Actions {
		if a.X != nil || a.Type == "click" || a.Type == "drag" || a.Type == "scroll" {
			f, ok := s.frames[r.FrameID]
			if !ok || m.now().Sub(f.CapturedAt) > 60*time.Second {
				m.mu.Unlock()
				return ActionResult{}, toolerr.New("conflict", "需要当前会话的新截图", "reason", "stale_frame")
			}
			if *a.X < 0 || *a.X >= f.Width || *a.Y < 0 || *a.Y >= f.Height || (a.ToX != nil && (*a.ToX < 0 || *a.ToX >= f.Width || *a.ToY < 0 || *a.ToY >= f.Height)) {
				m.mu.Unlock()
				return ActionResult{}, toolerr.Args("actions.coordinates")
			}
			frame = &f
		}
	}
	select {
	case m.queue <- struct{}{}:
	default:
		m.mu.Unlock()
		return ActionResult{}, toolerr.New("resource_busy", "桌面动作队列已满", "resource", "computer:queue")
	}
	op := &operation{digest: digest, result: ActionResult{ActionID: r.ActionID, State: "accepted"}, done: make(chan struct{}), previous: m.tail}
	m.tail = op.done
	s.actions[r.ActionID] = op
	m.workers.Add(1)
	go m.execute(s, op, r, frame)
	m.mu.Unlock()
	return m.wait(ctx, s, op)
}

func (m *Manager) wait(ctx context.Context, s *ownedSession, op *operation) (ActionResult, error) {
	select {
	case <-op.done:
	case <-ctx.Done():
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return materialize(s, op.result), nil
}

func (m *Manager) execute(s *ownedSession, op *operation, r ActRequest, frame *Frame) {
	defer m.workers.Done()
	defer func() { <-m.queue }()
	result := ActionResult{ActionID: r.ActionID, State: "failed"}
	defer func() {
		m.mu.Lock()
		if result.Frame != nil {
			f := *result.Frame
			f.Image = nil
			result.Frame = &f
		}
		op.result = result
		close(op.done)
		m.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	select {
	case <-op.previous:
	case <-ctx.Done():
		result.Error = "cancelled"
		return
	}
	select {
	case m.executing <- struct{}{}:
		defer func() { <-m.executing }()
	case <-ctx.Done():
		result.Error = "cancelled"
		return
	}
	m.mu.Lock()
	_, err := m.sessionLocked(s.Owner, r.SessionRequest, true)
	m.mu.Unlock()
	if err != nil {
		result.Error = errorCode(err)
		return
	}
	info, err := m.Info(ctx)
	if err == nil {
		err = desktopReady(info)
	}
	if err != nil {
		result.Error = errorCode(err)
		m.invalidate(s.ID)
		return
	}
	if info.Epoch != s.Epoch || (frame != nil && info.LayoutVersion != frame.LayoutVersion) {
		result.Error = "stale_frame"
		return
	}
	if err = ctx.Err(); err != nil {
		result.Error = "cancelled"
		return
	}
	result, err = m.provider.Execute(ctx, Execution{Epoch: s.Epoch, Token: s.Token, ActionID: r.ActionID, Frame: frame, Actions: r.Actions})
	result.ActionID = r.ActionID
	if err != nil || (result.State != "executed" && result.State != "failed" && result.State != "unknown") {
		result.State = "unknown"
		result.Error = "execution_unknown"
	}
	if result.Executed < 0 || result.Executed > len(r.Actions) {
		result.State = "unknown"
		result.Error = "invalid_execution_result"
		result.Executed = 0
	}
	if result.State == "executed" && result.Executed != len(r.Actions) {
		result.State = "unknown"
		result.Error = "incomplete_execution"
	}
	if result.State == "executed" && r.ObserveAfter {
		f, e := m.Observe(ctx, s.Owner, ObserveRequest{SessionRequest: r.SessionRequest, CaptureRequest: CaptureRequest{DisplayID: displayOf(frame)}})
		if e == nil {
			result.Frame = &f
		} else {
			result.Error = "observation_failed"
		}
	}
}

func displayOf(f *Frame) string {
	if f != nil {
		return f.DisplayID
	}
	return ""
}
func errorCode(err error) string {
	var e *toolerr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "desktop_unavailable"
}

func (m *Manager) ActionStatus(caller string, r StatusRequest) (ActionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.sessionLocked(caller, r.SessionRequest, false)
	if err != nil {
		return ActionResult{}, err
	}
	op := s.actions[r.ActionID]
	if op == nil {
		return ActionResult{}, toolerr.New("not_found", "动作不存在", "name", r.ActionID)
	}
	return materialize(s, op.result), nil
}

// Cached actions retain only frame metadata; the bounded frame cache owns image bytes.
func materialize(s *ownedSession, r ActionResult) ActionResult {
	if r.Frame != nil {
		f := *r.Frame
		if cached, ok := s.frames[f.ID]; ok {
			f.Image = cached.Image
		}
		r.Frame = &f
	}
	return r
}

func (m *Manager) CloseSession(caller string, r SessionRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.sessionLocked(caller, r, false)
	if err != nil {
		return err
	}
	if s.Mode == "control" && s.Token != r.Token {
		return toolerr.New("lease_required", "控制租约已失效")
	}
	m.removeLocked(s.ID)
	return nil
}

func (m *Manager) Sessions(caller string) []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	out := []Session{}
	for _, s := range m.sessions {
		if caller == "" || s.Owner == caller {
			out = append(out, s.Session)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) invalidate(id string) { m.mu.Lock(); defer m.mu.Unlock(); m.removeLocked(id) }

func (m *Manager) removeLocked(id string) {
	s := m.sessions[id]
	if s == nil {
		return
	}
	s.cancel()
	delete(m.sessions, id)
	if m.controller == id {
		m.controller = ""
		// Cancellation fences queued work immediately. Input cleanup is asynchronous and must
		// not hold the authority mutex while a provider waits for a running native action.
		m.workers.Add(1)
		go func() {
			defer m.workers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = m.provider.Release(ctx, Execution{Epoch: s.Epoch, Token: s.Token})
		}()
	}
}

func (m *Manager) expireLocked() {
	for id, s := range m.sessions {
		if !s.ExpiresAt.After(m.now()) {
			m.removeLocked(id)
		}
	}
}
func (m *Manager) revokeLocked(caller string) {
	for id, s := range m.sessions {
		if caller == "" || s.Owner == caller {
			m.removeLocked(id)
		}
	}
}
func (m *Manager) Disconnect(caller string) { m.mu.Lock(); defer m.mu.Unlock(); m.revokeLocked(caller) }
func (m *Manager) Revoke(id string)         { m.mu.Lock(); defer m.mu.Unlock(); m.removeLocked(id) }
func (m *Manager) Pause(paused bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = paused
	if paused {
		m.revokeLocked("")
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.stop)
	m.revokeLocked("")
	m.mu.Unlock()
	<-m.done
	m.workers.Wait()
	if m.provider != nil {
		return m.provider.Close()
	}
	return nil
}

func (m *Manager) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("computer(enabled=%t paused=%t sessions=%d)", m.settings.Enabled, m.paused, len(m.sessions))
}
