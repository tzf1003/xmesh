package devicelink

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
)

const LinkPath = "/mesh/v2/link"
const Subprotocol = "xmesh-link-v2"

var (
	ErrUnavailable      = errors.New("设备连接不可用")
	ErrRestarted        = errors.New("设备已重启，原会话已结束")
	ErrResumeExpired    = errors.New("设备会话恢复窗口已到期")
	ErrIdentityConflict = errors.New("设备身份对应多个运行实例，请检查设备状态目录")
	ErrLinkSilent       = errors.New("设备链路长时间没有收到数据")
)

type Options struct {
	Identity  *device.Identity
	Trust     *device.TrustStore
	Directory *device.Directory
	BootID    string
	// Dial opens a pinned mTLS WebSocket on the selected OS or tsnet path.
	Dial func(context.Context, string, device.Path) (*websocket.Conn, error)
	// Accept binds an incoming logical connection to an Agent without tying it to a carrier.
	Accept         func(context.Context, identity.Principal, *Session) error
	RecoveryWindow time.Duration
	// Heartbeat is how often a ping measures the link; any inbound frame or pong proves it alive.
	Heartbeat time.Duration
	// DeadAfter ends a carrier that has received nothing for this long. Default 30s.
	DeadAfter time.Duration
	// OnActivity runs under the device lock to retain transition order. It must not reenter Manager.
	OnActivity func(Activity)
}

type frame struct {
	Type         string `json:"type"`
	DeviceID     string `json:"device_id,omitempty"`
	BootID       string `json:"boot_id,omitempty"`
	Epoch        uint64 `json:"epoch,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	OpenID       string `json:"open_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Token        string `json:"token,omitempty"`
	OAuthClient  string `json:"oauth_client,omitempty"`
	Sequence     uint64 `json:"sequence,omitempty"`
	Ack          uint64 `json:"ack,omitempty"`
	Payload      []byte `json:"payload,omitempty"`
	// Part and Parts split one large data packet into consecutive frames so a pong never
	// waits behind a multi-megabyte write. Parts is zero for an unsplit packet.
	Part  uint32 `json:"part,omitempty"`
	Parts uint32 `json:"parts,omitempty"`
	Error string `json:"error,omitempty"`
}

type logical struct {
	session              *Session
	openID, token, oauth string
	incoming             bool
	ready                bool
	err                  error
	changed              chan struct{}
	trustGeneration      uint64
	attachedEpoch        uint64
	closedAt             time.Time
	detachedAt           time.Time
}

type carrier struct {
	conn      *websocket.Conn
	epoch     uint64
	id        string
	path      device.Path
	cancel    context.CancelFunc
	committed bool
	// seen is the unix-nano time of the last inbound frame or pong.
	seen atomic.Int64
}

type pathFailure struct {
	generation uint64
	failures   int
	until      time.Time
}

type peerLink struct {
	mu           sync.Mutex
	id, boot     string
	epoch        uint64
	active       *carrier
	sessions     map[string]*logical
	opens        map[string]*logical
	pending      map[string]*logical
	control      []frame
	changed      chan struct{}
	wake         chan struct{}
	disconnected time.Time
	attempting   bool
	closed       bool
	conflicted   bool
	unstable     bool
	revoked      bool
	failures     map[string]pathFailure
	workers      sync.WaitGroup
}

// Manager owns one physical carrier for each device pair and isolated logical sessions on it.
// A healthy carrier is sticky. Physical errors reconnect it without closing the SDK sessions.
type Manager struct {
	mu       sync.Mutex
	opts     Options
	ctx      context.Context
	cancel   context.CancelFunc
	peers    map[string]*peerLink
	budget   *Budget
	workers  sync.WaitGroup
	closed   bool
	stopping atomic.Bool
}

func NewManager(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Identity == nil || opts.Trust == nil || opts.Directory == nil || opts.Dial == nil {
		return nil, fmt.Errorf("设备链路配置不完整")
	}
	if opts.BootID == "" {
		opts.BootID = rand.Text()
	}
	if opts.RecoveryWindow <= 0 {
		opts.RecoveryWindow = 120 * time.Second
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 5 * time.Second
	}
	if opts.DeadAfter <= 0 {
		opts.DeadAfter = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{opts: opts, ctx: ctx, cancel: cancel, peers: map[string]*peerLink{}, budget: NewBudget(0)}
	changed, stop := opts.Trust.Subscribe()
	m.workers.Go(func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-changed:
				if !ok {
					return
				}
				m.reauthorize()
			}
		}
	})
	return m, nil
}

func (m *Manager) BootID() string { return m.opts.BootID }

type Activity struct {
	DeviceID string
	Path     device.Path
	Epoch    uint64
	State    string
}

func (m *Manager) Activity(id string) Activity {
	m.mu.Lock()
	p := m.peers[id]
	m.mu.Unlock()
	if p == nil {
		return Activity{DeviceID: id, State: "offline"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conflicted {
		return Activity{DeviceID: id, State: "identity_conflict"}
	}
	if p.active != nil && p.active.committed {
		state := "online"
		if p.unstable {
			state = "unstable"
		}
		return Activity{DeviceID: id, Path: p.active.path, Epoch: p.active.epoch, State: state}
	}
	if !p.disconnected.IsZero() && time.Since(p.disconnected) < m.opts.RecoveryWindow {
		for _, l := range p.pending {
			if l.err == nil {
				return Activity{DeviceID: id, Epoch: p.epoch, State: "reconnecting"}
			}
		}
		for _, l := range p.sessions {
			if l.err == nil && !l.session.Closed() {
				return Activity{DeviceID: id, Epoch: p.epoch, State: "reconnecting"}
			}
		}
	}
	return Activity{DeviceID: id, State: "offline"}
}

// DisablePath retires only carriers on a path that the runtime has stopped. Logical sessions
// remain available to a provider that can still reach the same trusted device.
func (m *Manager) DisablePath(kind device.PathKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.peers {
		p.mu.Lock()
		if p.active != nil && p.active.path.Kind == kind {
			p.active.cancel()
			_ = p.active.conn.CloseNow()
		}
		p.signalLocked()
		p.mu.Unlock()
	}
}

func (m *Manager) peer(id string) (*peerLink, error) {
	if id == m.opts.Identity.ID {
		return nil, fmt.Errorf("设备不能连接自身")
	}
	if _, ok := m.opts.Trust.Find(id); !ok {
		return nil, device.ErrUntrusted
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return nil, context.Canceled
	}
	p := m.peers[id]
	if p == nil {
		p = &peerLink{id: id, sessions: map[string]*logical{}, opens: map[string]*logical{}, pending: map[string]*logical{}, failures: map[string]pathFailure{}, changed: make(chan struct{}), wake: make(chan struct{}, 1)}
		m.peers[id] = p
		m.workers.Go(func() { m.runPeer(p) })
	}
	return p, nil
}

func (p *peerLink) signalLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Open creates a separate MCP logical session for this upstream and target device.
func (m *Manager) Open(ctx context.Context, id, oauthClient string) (*Transport, error) {
	if m.opts.Directory.IdentityConflict(id, time.Now()) {
		return nil, ErrIdentityConflict
	}
	p, err := m.peer(id)
	if err != nil {
		return nil, err
	}
	trusted, _ := m.opts.Trust.Find(id)
	l := &logical{openID: rand.Text(), oauth: oauthClient, changed: make(chan struct{}), trustGeneration: trusted.Generation}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrUnavailable
	}
	if len(p.pending)+len(p.sessions) >= maxLogicalSessions {
		p.mu.Unlock()
		return nil, ErrBackpressure
	}
	if p.active == nil && len(p.pending) == 0 && len(p.sessions) == 0 {
		p.disconnected = time.Time{}
	}
	p.pending[l.openID] = l
	p.signalLocked()
	p.mu.Unlock()
	for {
		p.mu.Lock()
		ready, err, changed := l.ready, l.err, l.changed
		if ready && err == nil {
			session := l.session
			p.mu.Unlock()
			return &Transport{Session: session}, nil
		}
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			p.mu.Lock()
			delete(p.pending, l.openID)
			l.err = ctx.Err()
			if l.session != nil {
				_ = l.session.Close()
			}
			p.signalLocked()
			p.mu.Unlock()
			return nil, ctx.Err()
		case <-m.ctx.Done():
			return nil, context.Canceled
		case <-changed:
		}
	}
}

func (m *Manager) reauthorize() {
	m.mu.Lock()
	peers := make([]*peerLink, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	m.mu.Unlock()
	for _, p := range peers {
		trusted, ok := m.opts.Trust.Find(p.id)
		p.mu.Lock()
		if !ok && !p.revoked {
			m.activity(Activity{DeviceID: p.id, State: "revoked", Epoch: p.epoch})
		}
		p.revoked = !ok
		for _, l := range p.sessions {
			if !ok || trusted.Generation != l.trustGeneration {
				closeLogical(l, device.ErrUntrusted)
			}
		}
		for _, l := range p.pending {
			if !ok || trusted.Generation != l.trustGeneration {
				closeLogical(l, device.ErrUntrusted)
			}
		}
		if !ok && p.active != nil {
			if err := p.queueLocked(frame{Type: "revoked"}); err != nil {
				p.active.cancel()
				_ = p.active.conn.CloseNow()
			}
		}
		p.signalLocked()
		p.mu.Unlock()
	}
}

func closeLogical(l *logical, err error) {
	if l.err != nil {
		return
	}
	l.err = err
	if l.session != nil {
		_ = l.session.Close()
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

func (m *Manager) candidates(id string) []device.Path {
	devices, _ := m.opts.Directory.Snapshot(time.Now())
	for _, d := range devices {
		if d.DeviceID == id {
			if d.IdentityConflict {
				return nil
			}
			paths := slices.Clone(d.Paths)
			return slices.DeleteFunc(paths, func(p device.Path) bool { return !p.Reachable })
		}
	}
	return nil
}

func (m *Manager) runPeer(p *peerLink) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	changed, stop := m.opts.Directory.Subscribe()
	defer stop()
	defer func() {
		p.mu.Lock()
		p.closed = true
		for _, l := range p.sessions {
			closeLogical(l, context.Canceled)
		}
		for _, l := range p.pending {
			closeLogical(l, context.Canceled)
		}
		if p.active != nil {
			p.active.cancel()
			_ = p.active.conn.CloseNow()
		}
		p.mu.Unlock()
		p.workers.Wait()
	}()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		case _, ok := <-changed:
			if !ok {
				return
			}
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		if m.opts.Directory.IdentityConflict(p.id, time.Now()) {
			if !p.conflicted {
				p.conflicted = true
				m.activity(Activity{DeviceID: p.id, State: "identity_conflict", Epoch: p.epoch})
			}
			for _, l := range p.sessions {
				closeLogical(l, ErrIdentityConflict)
			}
			for _, l := range p.pending {
				closeLogical(l, ErrIdentityConflict)
			}
			if p.active != nil {
				p.active.cancel()
				_ = p.active.conn.CloseNow()
			}
			p.mu.Unlock()
			continue
		}
		p.conflicted = false
		wanted := false
		for key, l := range p.pending {
			if l.err != nil {
				delete(p.pending, key)
			} else {
				wanted = true
			}
		}
		for sid, l := range p.sessions {
			if l.incoming && !l.detachedAt.IsZero() && time.Since(l.detachedAt) >= m.opts.RecoveryWindow {
				closeLogical(l, ErrResumeExpired)
			}
			if l.err == nil && !l.session.Closed() {
				wanted = true
			} else {
				if l.closedAt.IsZero() {
					l.closedAt = time.Now()
				}
				if time.Since(l.closedAt) < m.opts.RecoveryWindow {
					wanted = true
				} else {
					delete(p.sessions, sid)
					delete(p.opens, l.openID)
				}
			}
		}
		if wanted && !p.disconnected.IsZero() && time.Since(p.disconnected) >= m.opts.RecoveryWindow {
			for _, l := range p.sessions {
				closeLogical(l, ErrResumeExpired)
			}
			for _, l := range p.pending {
				closeLogical(l, ErrResumeExpired)
			}
			if p.active != nil && !p.active.committed {
				p.active.cancel()
				_ = p.active.conn.CloseNow()
			}
			m.activity(Activity{DeviceID: p.id, State: "offline", Epoch: p.epoch})
			p.mu.Unlock()
			continue
		}
		if p.active == nil && wanted {
			if p.disconnected.IsZero() {
				p.disconnected = time.Now()
			}
			if !p.attempting {
				p.attempting = true
				p.workers.Go(func() { m.connect(p); p.mu.Lock(); p.attempting = false; p.mu.Unlock() })
			}
		}
		p.mu.Unlock()
	}
}

func (m *Manager) connect(p *peerLink) {
	if m.preferLAN(m.ctx, p) {
		return
	}
	m.connectPaths(m.ctx, p, m.candidates(p.id), device.Tailnet, 5*time.Second)
}

// Give authenticated LAN discovery the same bounded opportunity for local opens
// and incoming Tailnet candidates. An existing carrier always wins arbitration.
func (m *Manager) preferLAN(ctx context.Context, p *peerLink) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		active := p.active != nil || p.closed
		p.mu.Unlock()
		if active {
			return true
		}
		if _, enabled := m.opts.Directory.Provider(device.LAN); !enabled {
			return false
		}
		paths := m.candidates(p.id)
		if slices.ContainsFunc(paths, func(path device.Path) bool { return path.Kind == device.LAN }) {
			return m.connectPaths(ctx, p, paths, device.LAN, 2*time.Second)
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func (m *Manager) connectPaths(ctx context.Context, p *peerLink, paths []device.Path, kind device.PathKind, budget time.Duration) bool {
	kindContext, stop := context.WithTimeout(ctx, budget)
	defer stop()
	for _, path := range paths {
		if path.Kind != kind {
			continue
		}
		p.mu.Lock()
		active, closed := p.active != nil, p.closed
		key := string(path.Kind) + "\x00" + path.Address
		failure := p.failures[key]
		cooling := failure.generation == path.ProviderGeneration && time.Now().Before(failure.until)
		p.mu.Unlock()
		if active || closed || m.ctx.Err() != nil {
			return true
		}
		if cooling {
			continue
		}
		if kindContext.Err() != nil {
			return false
		}
		ctx, cancel := context.WithCancel(kindContext)
		conn, err := m.opts.Dial(ctx, p.id, path)
		if err == nil {
			err = m.handshake(ctx, p, conn, path, true)
		}
		cancel()
		if err == nil {
			p.mu.Lock()
			delete(p.failures, key)
			p.mu.Unlock()
			return true
		}
		if conn != nil {
			_ = conn.CloseNow()
		}
		p.mu.Lock()
		if p.active == nil && !p.closed && m.ctx.Err() == nil {
			if failure.generation != path.ProviderGeneration {
				failure = pathFailure{generation: path.ProviderGeneration}
			}
			failure.failures++
			failure.until = time.Now().Add(min(time.Second*time.Duration(uint64(1)<<min(failure.failures-1, 5)), 30*time.Second))
			p.failures[key] = failure
		}
		p.mu.Unlock()
	}
	return false
}

func writeFrame(ctx context.Context, conn *websocket.Conn, value frame) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageBinary, raw)
}

func readFrame(ctx context.Context, conn *websocket.Conn) (frame, error) {
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return frame{}, err
	}
	var value frame
	err = json.Unmarshal(raw, &value)
	return value, err
}

// Handler binds the listener's path kind rather than accepting a client-supplied network label.
func (m *Manager) Handler(kind device.PathKind) http.Handler {
	return m.handler(kind, 0)
}

// HandlerAt binds an HTTP listener to the provider generation that created it.
// A request queued on a retired listener cannot adopt a replacement's generation.
func (m *Manager) HandlerAt(kind device.PathKind, generation uint64) http.Handler {
	return m.handler(kind, generation)
}

func (m *Manager) handler(kind device.PathKind, listenerGeneration uint64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LinkPath {
			http.NotFound(w, r)
			return
		}
		generation, enabled := m.opts.Directory.Provider(kind)
		if !enabled || (listenerGeneration != 0 && generation != listenerGeneration) {
			http.Error(w, "连接路径已关闭", 503)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			http.Error(w, "设备身份无效", 401)
			return
		}
		trusted, err := m.opts.Trust.VerifyCertificate(r.TLS.PeerCertificates[0], "")
		if err != nil {
			http.Error(w, "设备尚未配对", 401)
			return
		}
		p, err := m.peer(trusted.Document.DeviceID)
		if err != nil {
			http.Error(w, "设备连接不可用", 503)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
		if err != nil {
			return
		}
		if conn.Subprotocol() != Subprotocol {
			_ = conn.CloseNow()
			return
		}
		conn.SetReadLimit((MaxFrameBytes * 4 / 3) + 4096)
		ctx, cancel := context.WithTimeout(m.ctx, 7*time.Second)
		err = m.handshake(ctx, p, conn, device.Path{Kind: kind, Address: r.RemoteAddr, ProviderGeneration: generation}, false)
		cancel()
		if err != nil {
			_ = conn.CloseNow()
		}
	})
}

func (m *Manager) handshake(ctx context.Context, p *peerLink, conn *websocket.Conn, path device.Path, outgoing bool) error {
	if m.opts.Directory.IdentityConflict(p.id, time.Now()) {
		return ErrIdentityConflict
	}
	generation, enabled := m.opts.Directory.Provider(path.Kind)
	if !enabled || generation != path.ProviderGeneration {
		return device.ErrStalePath
	}
	if !outgoing && path.Kind == device.Tailnet && m.preferLAN(ctx, p) {
		return ErrUnavailable
	}
	if outgoing {
		conn.SetReadLimit((MaxFrameBytes * 4 / 3) + 4096)
	}
	hello := frame{Type: "hello", DeviceID: m.opts.Identity.ID, BootID: m.opts.BootID, ConnectionID: rand.Text()}
	var remote frame
	var err error
	if outgoing {
		if err = writeFrame(ctx, conn, hello); err != nil {
			return err
		}
		remote, err = readFrame(ctx, conn)
	} else {
		remote, err = readFrame(ctx, conn)
		if err == nil {
			err = writeFrame(ctx, conn, hello)
		}
	}
	if err != nil {
		return err
	}
	if remote.Type != "hello" || remote.DeviceID != p.id || remote.BootID == "" {
		return device.ErrUntrusted
	}
	coordinator := m.opts.Identity.ID < p.id
	var commit frame
	if coordinator {
		p.mu.Lock()
		if p.closed || p.active != nil {
			p.mu.Unlock()
			return ErrUnavailable
		}
		p.epoch++
		commit = frame{Type: "commit", Epoch: p.epoch, ConnectionID: hello.ConnectionID, BootID: m.opts.BootID}
		// Reserve the arbitration slot through its confirmation, blocking concurrent candidates.
		carrierCtx, cancel := context.WithCancel(m.ctx)
		c := &carrier{conn: conn, epoch: commit.Epoch, id: commit.ConnectionID, path: path, cancel: cancel}
		p.active = c
		p.mu.Unlock()
		if err = writeFrame(ctx, conn, commit); err == nil {
			var ack frame
			ack, err = readFrame(ctx, conn)
			if err == nil && (ack.Type != "committed" || ack.Epoch != commit.Epoch || ack.ConnectionID != commit.ConnectionID) {
				err = ErrSequence
			}
		}
		if err != nil {
			cancel()
			p.mu.Lock()
			if p.active == c {
				p.active = nil
			}
			p.mu.Unlock()
			return err
		}
		return m.activate(p, c, carrierCtx, remote.BootID)
	}
	commit, err = readFrame(ctx, conn)
	if err != nil {
		return err
	}
	if commit.Type != "commit" || commit.Epoch == 0 || commit.ConnectionID == "" || commit.BootID != remote.BootID {
		return ErrSequence
	}
	p.mu.Lock()
	if p.closed || p.active != nil || (p.boot == remote.BootID && commit.Epoch <= p.epoch) {
		p.mu.Unlock()
		return ErrUnavailable
	}
	carrierCtx, cancel := context.WithCancel(m.ctx)
	c := &carrier{conn: conn, epoch: commit.Epoch, id: commit.ConnectionID, path: path, cancel: cancel}
	p.active = c
	p.epoch = commit.Epoch
	p.mu.Unlock()
	if err := writeFrame(ctx, conn, frame{Type: "committed", Epoch: commit.Epoch, ConnectionID: commit.ConnectionID}); err != nil {
		cancel()
		p.mu.Lock()
		if p.active == c {
			p.active = nil
		}
		p.mu.Unlock()
		return err
	}
	return m.activate(p, c, carrierCtx, remote.BootID)
}

func (m *Manager) activate(p *peerLink, c *carrier, ctx context.Context, boot string) error {
	p.mu.Lock()
	if !p.disconnected.IsZero() && time.Since(p.disconnected) >= m.opts.RecoveryWindow {
		for _, l := range p.sessions {
			closeLogical(l, ErrResumeExpired)
		}
		for _, l := range p.pending {
			closeLogical(l, ErrResumeExpired)
		}
		c.cancel()
		_ = c.conn.CloseNow()
		if p.active == c {
			p.active = nil
			p.signalLocked()
		}
		p.mu.Unlock()
		return ErrResumeExpired
	}
	generation, enabled := m.opts.Directory.Provider(c.path.Kind)
	_, trusted := m.opts.Trust.Find(p.id)
	if p.closed || p.active != c || m.ctx.Err() != nil || m.stopping.Load() || !enabled || generation != c.path.ProviderGeneration || !trusted || m.opts.Directory.IdentityConflict(p.id, time.Now()) {
		c.cancel()
		_ = c.conn.CloseNow()
		if p.active == c {
			p.active = nil
			p.signalLocked()
		}
		p.mu.Unlock()
		return ErrUnavailable
	}
	if p.boot != "" && p.boot != boot {
		for _, l := range p.sessions {
			closeLogical(l, ErrRestarted)
		}
		for _, l := range p.pending {
			closeLogical(l, ErrRestarted)
		}
	}
	for _, l := range p.sessions {
		if l.incoming && l.attachedEpoch != c.epoch && l.detachedAt.IsZero() {
			l.detachedAt = p.disconnected
			if l.detachedAt.IsZero() {
				l.detachedAt = time.Now()
			}
		}
	}
	p.boot = boot
	c.committed = true
	p.control = nil
	p.disconnected = time.Time{}
	p.unstable = false
	p.signalLocked()
	m.activity(Activity{DeviceID: p.id, Path: c.path, Epoch: c.epoch, State: "online"})
	p.workers.Go(func() { m.serveCarrier(ctx, p, c) })
	p.mu.Unlock()
	return nil
}

func (m *Manager) serveCarrier(ctx context.Context, p *peerLink, c *carrier) {
	defer func() {
		c.cancel()
		_ = c.conn.CloseNow()
		p.mu.Lock()
		if p.active == c {
			p.active = nil
			p.unstable = false
			p.disconnected = time.Now()
			state := "offline"
			if !p.closed && m.ctx.Err() == nil {
				for _, l := range p.pending {
					if l.err == nil {
						state = "reconnecting"
					}
				}
				for _, l := range p.sessions {
					if l.err == nil && !l.session.Closed() {
						state = "reconnecting"
					}
				}
			}
			m.activity(Activity{DeviceID: p.id, Path: c.path, Epoch: c.epoch, State: state})
			p.signalLocked()
		}
		p.mu.Unlock()
	}()
	errCh := make(chan error, 2)
	go func() { errCh <- m.readCarrier(ctx, p, c) }()
	go func() { errCh <- m.writeCarrier(ctx, p, c) }()
	consumed := false
	select {
	case <-ctx.Done():
	case <-errCh:
		consumed = true
	}
	c.cancel()
	_ = c.conn.CloseNow()
	if !consumed {
		<-errCh
	}
	<-errCh
}

func (m *Manager) activity(activity Activity) {
	if m.opts.OnActivity != nil {
		m.opts.OnActivity(activity)
	}
}

// Close ends logical sessions and joins the workers; replacing a carrier never calls it.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.workers.Wait()
		return nil
	}
	m.closed = true
	m.stopping.Store(true)
	active := []*carrier{}
	for _, p := range m.peers {
		p.mu.Lock()
		if p.active != nil && p.active.committed {
			active = append(active, p.active)
		}
		p.mu.Unlock()
	}
	m.mu.Unlock()
	// A clean process exit ends its logical sessions. Network loss instead keeps
	// them recoverable; the bounded shutdown notice distinguishes those cases.
	ctx, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
	var notifications sync.WaitGroup
	for _, c := range active {
		notifications.Go(func() { _ = writeFrame(ctx, c.conn, frame{Type: "shutdown", Epoch: c.epoch}) })
	}
	notifications.Wait()
	stop()
	m.mu.Lock()
	m.cancel()
	for _, p := range m.peers {
		p.mu.Lock()
		p.closed = true
		for _, l := range p.sessions {
			closeLogical(l, context.Canceled)
		}
		for _, l := range p.pending {
			closeLogical(l, context.Canceled)
		}
		if p.active != nil {
			p.active.cancel()
			_ = p.active.conn.CloseNow()
		}
		p.signalLocked()
		p.mu.Unlock()
	}
	m.mu.Unlock()
	m.workers.Wait()
	return nil
}

func tokenEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
