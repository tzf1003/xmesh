package devicelink

import (
	"cmp"
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
	// PathOrder returns the paths to use, most preferred first, without the disabled ones. It is
	// read on every connection attempt, so a change applies to the next one. Nil means LAN,
	// Tailnet, then Cloudflare.
	PathOrder func() []device.PathKind
	// Smart reports whether paths are chosen by measured quality (loss, drops, latency) rather than
	// by PathOrder alone. PathOrder still lists the enabled paths and breaks ties. Read on each decision.
	Smart func() bool
	// Switching tunes how smart mode moves a live carrier; zero fields take the defaults.
	Switching Switching
	// OnPathSwitch runs, outside any lock, after smart mode drops a carrier for a better path. It must
	// not reenter Manager.
	OnPathSwitch func(deviceID string, from, to device.PathKind, reason string)
	// QualityEvery is how often this end tells the device how well the paths it dials are measured. Default 15s.
	QualityEvery time.Duration
	// OnActivity runs under the device lock to retain transition order. It must not reenter Manager.
	OnActivity func(Activity)
	// IdleClose is how long the second carrier of a device may carry nothing before the end that dialed
	// it closes it. A device's only carrier is never closed for idleness. Default 5 minutes.
	IdleClose time.Duration
	// MigrateIdle is how long a logical session borrowing the other slot's carrier must be quiet before
	// it moves back to its own slot's. Default 2s.
	MigrateIdle time.Duration
	// SuspectAfter is how long a carrier may receive nothing, not even a pong, before sessions leave it
	// for a healthy carrier and a replacement is dialed. Default three heartbeats, at most half of DeadAfter.
	SuspectAfter time.Duration
	// HandoffTimeout bounds how long a carrier being replaced stays open for its sessions to move. Default 10s.
	HandoffTimeout time.Duration
	// PeerDialDelay is how long an end that opened nothing itself waits, with no usable carrier, for the
	// other end to dial before it dials too. Default 2s, at most a quarter of RecoveryWindow.
	PeerDialDelay time.Duration
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
	// Caps are the optional features a hello announces; an end only uses a feature the other announced,
	// so a peer that predates one never sees its frames.
	Caps []string `json:"caps,omitempty"`
	// Quality carries a "quality" frame: the sender's measurements of the paths it dials. Display only.
	Quality []device.KindQuality `json:"quality,omitempty"`
}

// On "open" and "resume" frames Sequence numbers the attempt, and the matching "opened" and "resumed"
// echo it, so a reply that crossed a newer attempt on the other carrier is ignored. Zero means unnumbered.

const (
	// capPathQuality announces that an end sends and accepts "quality" frames.
	capPathQuality = "path-quality-v1"
	// capDualSlot announces that an end keeps one carrier for each dialing direction. A peer without it
	// gets one carrier per device pair, as before.
	capDualSlot = "dual-slot-v1"
)

type logical struct {
	session              *Session
	openID, token, oauth string
	incoming             bool
	ready                bool
	err                  error
	changed              chan struct{}
	trustGeneration      uint64
	// attached is the carrier the session's data runs on; nil while it waits for one.
	attached *carrier
	// openOn and resumeOn are the carriers the latest open and resume went out on, gen their number.
	openOn, resumeOn *carrier
	gen              uint64
	closedAt         time.Time
	detachedAt       time.Time
	// active is when data last moved, which decides whether the session is idle enough to change carrier.
	active time.Time
}

type carrier struct {
	conn      *websocket.Conn
	epoch     uint64
	id        string
	path      device.Path
	cancel    context.CancelFunc
	committed bool
	// outgoing is true on the end that dialed; only that end decides to move the carrier.
	outgoing bool
	started  time.Time
	// peerQuality is true when the other end announced capPathQuality.
	peerQuality bool
	// seen is the unix-nano time of the last inbound frame or pong.
	seen atomic.Int64
	// suspect is set while nothing has arrived for SuspectAfter: sessions prefer another carrier.
	suspect atomic.Bool
	// The fields below are guarded by the device lock.
	control    []frame
	unstable   bool
	draining   bool
	drainSince time.Time
	busyAt     time.Time
	// done is closed once the carrier's worker has finished with the connection.
	done chan struct{}
}

type pathFailure struct {
	generation uint64
	failures   int
	until      time.Time
}

// handoff is a pending replacement of the carrier this end dialed: the new one is dialed while the old
// one still carries the sessions, and the old one is retired once the new one commits.
type handoff struct {
	from   *carrier
	to     device.PathKind
	reason string
}

type peerLink struct {
	mu       sync.Mutex
	id, boot string
	// epoch numbers commits; the end with the smaller device ID assigns it. lastEpoch is the newest
	// epoch accepted for each dialing direction (see dirIndex) by the end that does not assign.
	epoch     uint64
	lastEpoch [2]uint64
	// cars holds at most one live carrier per dialing direction, plus a handshake in progress and
	// carriers being replaced.
	cars         []*carrier
	sessions     map[string]*logical
	opens        map[string]*logical
	pending      map[string]*logical
	changed      chan struct{}
	wake         chan struct{}
	disconnected time.Time
	lastUsable   time.Time
	attempting   bool
	closed       bool
	conflicted   bool
	revoked      bool
	// latestBoot is the boot ID in the newest hello; a handshake that ends with another one is from a dead run.
	latestBoot string
	// legacy is true when the peer's hello lacked capDualSlot, so a device pair keeps one carrier.
	legacy   bool
	handoff  *handoff
	failures map[string]pathFailure
	// betterKind has beaten the active path since betterSince; smart mode moves only after it holds.
	betterKind  device.PathKind
	betterSince time.Time
	workers     sync.WaitGroup
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
	if opts.QualityEvery <= 0 {
		opts.QualityEvery = 15 * time.Second
	}
	if opts.IdleClose <= 0 {
		opts.IdleClose = 5 * time.Minute
	}
	if opts.MigrateIdle <= 0 {
		opts.MigrateIdle = 2 * time.Second
	}
	if opts.SuspectAfter <= 0 {
		opts.SuspectAfter = min(3*opts.Heartbeat, opts.DeadAfter/2)
	}
	if opts.HandoffTimeout <= 0 {
		opts.HandoffTimeout = 10 * time.Second
	}
	if opts.PeerDialDelay <= 0 {
		opts.PeerDialDelay = min(2*time.Second, opts.RecoveryWindow/4)
	}
	opts.Switching = opts.Switching.withDefaults()
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{opts: opts, ctx: ctx, cancel: cancel, peers: map[string]*peerLink{}, budget: NewBudget(0)}
	if opts.Smart != nil {
		m.workers.Go(m.reviewLoop)
	}
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
	// Outgoing is true when this end dialed the active carrier.
	Outgoing bool
}

// CarriesPath reports whether a healthy carrier this end dialed runs on exactly this path. Its heartbeat
// already measures the path, so probing it again would only add traffic.
func (m *Manager) CarriesPath(id string, kind device.PathKind, address string) bool {
	m.mu.Lock()
	p := m.peers[id]
	m.mu.Unlock()
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.ContainsFunc(p.cars, func(c *carrier) bool {
		return c.usable() && c.outgoing && !c.unstable && c.path.Kind == kind && c.path.Address == address
	})
}

// Activity reports the device's link: its primary carrier, the healthy one this end dialed if there is one.
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
	if c := p.primaryLocked(); c != nil {
		return Activity{DeviceID: id, Path: c.path, Epoch: c.epoch, State: c.state(), Outgoing: c.outgoing}
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

func (c *carrier) state() string {
	if c.unstable {
		return "unstable"
	}
	return "online"
}

// DisablePath retires only carriers on a path that the runtime has stopped. Logical sessions
// remain available to a provider that can still reach the same trusted device.
func (m *Manager) DisablePath(kind device.PathKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.peers {
		p.mu.Lock()
		for _, c := range p.cars {
			if c.path.Kind == kind {
				c.cancel()
				_ = c.conn.CloseNow()
			}
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
	if len(p.cars) == 0 && len(p.pending) == 0 && len(p.sessions) == 0 {
		p.disconnected = time.Time{}
	}
	l.active = time.Now()
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
		if !ok {
			for _, c := range p.cars {
				if err := p.queueLocked(c, frame{Type: "revoked"}); err != nil {
					c.cancel()
					_ = c.conn.CloseNow()
				}
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
			paths = slices.DeleteFunc(paths, func(p device.Path) bool { return !p.Reachable || m.rank(p.Kind) < 0 })
			if m.smart() {
				sortByScore(paths)
			}
			return paths
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
		for _, c := range p.cars {
			c.cancel()
			_ = c.conn.CloseNow()
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
			for _, c := range p.cars {
				c.cancel()
				_ = c.conn.CloseNow()
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
			for _, c := range p.cars {
				if !c.committed {
					c.cancel()
					_ = c.conn.CloseNow()
				}
			}
			m.activity(Activity{DeviceID: p.id, State: "offline", Epoch: p.epoch})
			p.mu.Unlock()
			continue
		}
		now := time.Now()
		m.maintainLocked(p, now)
		if wanted && len(p.cars) == 0 && p.disconnected.IsZero() {
			p.disconnected = now
		}
		if wanted && !p.attempting {
			if plan, ok := m.dialPlanLocked(p, now); ok {
				p.attempting = true
				p.workers.Go(func() { m.connect(p, plan); p.mu.Lock(); p.attempting = false; p.mu.Unlock() })
			}
		}
		p.mu.Unlock()
	}
}

func (m *Manager) connect(p *peerLink, plan dialPlan) {
	order := plan.kinds
	if order == nil {
		order = m.pathOrder()
		if m.smart() {
			order = m.smartOrder(p.id, order)
		}
	}
	for _, kind := range order {
		if m.dialKind(m.ctx, p, kind, 0, plan) {
			return
		}
	}
	if plan.replace != nil {
		// The replacement did not come up: the old carrier stays, and a switch must be earned again.
		p.mu.Lock()
		if p.handoff != nil && p.handoff.from == plan.replace {
			p.handoff = nil
		}
		p.betterKind, p.betterSince = "", time.Time{}
		p.mu.Unlock()
	}
}

// dialKind tries to open a carrier on one kind of path and reports whether the dial has done its work.
// A zero budget takes the kind's own: LAN waits for discovery, Tailnet 5 seconds, a direct address 8, and Cloudflare, which
// works with no inbound address but adds latency, 15.
func (m *Manager) dialKind(ctx context.Context, p *peerLink, kind device.PathKind, budget time.Duration, plan dialPlan) bool {
	switch kind {
	case device.LAN:
		return m.preferLAN(ctx, p, plan)
	case device.Tailnet:
		return m.connectPaths(ctx, p, m.candidates(p.id), kind, cmp.Or(budget, 5*time.Second), plan)
	case device.Direct:
		return m.connectPaths(ctx, p, m.candidates(p.id), kind, cmp.Or(budget, 8*time.Second), plan)
	case device.Cloudflare:
		return m.connectPaths(ctx, p, m.candidates(p.id), kind, cmp.Or(budget, 15*time.Second), plan)
	}
	return false
}

// pathOrder is the configured order of paths, or the default one.
func (m *Manager) pathOrder() []device.PathKind {
	if m.opts.PathOrder != nil {
		return m.opts.PathOrder()
	}
	return []device.PathKind{device.LAN, device.Tailnet, device.Direct, device.Cloudflare}
}

// rank is the position of kind in the path order, or -1 when the path is disabled.
func (m *Manager) rank(kind device.PathKind) int {
	return slices.Index(m.pathOrder(), kind)
}

// Rebalance drops each carrier that is not on the most preferred path currently reachable for its
// device, so a changed path order takes effect without waiting for the link to fail. The logical
// sessions resume on the new carrier. A carrier on a disabled path is always dropped. In smart mode
// only that last rule applies: the review loop judges the rest by measured quality.
func (m *Manager) Rebalance() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.peers {
		p.mu.Lock()
		for _, c := range p.cars {
			have := m.rank(c.path.Kind)
			better := slices.ContainsFunc(m.candidates(p.id), func(path device.Path) bool {
				r := m.rank(path.Kind)
				return r >= 0 && (have < 0 || r < have)
			})
			if have < 0 || (better && !m.smart()) {
				c.cancel()
				_ = c.conn.CloseNow()
			}
		}
		p.signalLocked()
		p.mu.Unlock()
	}
}

// Give authenticated LAN discovery the same bounded opportunity for local opens
// and incoming Tailnet candidates. An existing carrier always wins arbitration.
func (m *Manager) preferLAN(ctx context.Context, p *peerLink, plan dialPlan) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		done := m.dialDoneLocked(p, plan)
		p.mu.Unlock()
		if done {
			return true
		}
		if _, enabled := m.opts.Directory.Provider(device.LAN); !enabled {
			return false
		}
		paths := m.candidates(p.id)
		if slices.ContainsFunc(paths, func(path device.Path) bool { return path.Kind == device.LAN }) {
			return m.connectPaths(ctx, p, paths, device.LAN, 2*time.Second, plan)
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func (m *Manager) connectPaths(ctx context.Context, p *peerLink, paths []device.Path, kind device.PathKind, budget time.Duration, plan dialPlan) bool {
	kindContext, stop := context.WithTimeout(ctx, budget)
	defer stop()
	for _, path := range paths {
		if path.Kind != kind {
			continue
		}
		p.mu.Lock()
		done := m.dialDoneLocked(p, plan)
		key := string(path.Kind) + "\x00" + path.Address
		failure := p.failures[key]
		cooling := failure.generation == path.ProviderGeneration && time.Now().Before(failure.until)
		p.mu.Unlock()
		if done || m.ctx.Err() != nil {
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
			err = m.handshake(ctx, p, conn, path, true, plan.replace)
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
		if !m.dialDoneLocked(p, plan) && m.ctx.Err() == nil {
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
		err = m.handshake(ctx, p, conn, device.Path{Kind: kind, Address: r.RemoteAddr, ProviderGeneration: generation}, false, nil)
		cancel()
		if err != nil {
			_ = conn.CloseNow()
		}
	})
}

// handshake agrees an epoch for a new carrier and commits it into the slot of its dialing direction. The
// end with the smaller device ID assigns the epoch. replace is the carrier an outgoing dial takes over from.
func (m *Manager) handshake(ctx context.Context, p *peerLink, conn *websocket.Conn, path device.Path, outgoing bool, replace *carrier) error {
	if m.opts.Directory.IdentityConflict(p.id, time.Now()) {
		return ErrIdentityConflict
	}
	generation, enabled := m.opts.Directory.Provider(path.Kind)
	if !enabled || generation != path.ProviderGeneration {
		return device.ErrStalePath
	}
	if m.rank(path.Kind) < 0 {
		return ErrUnavailable
	}
	if outgoing {
		conn.SetReadLimit((MaxFrameBytes * 4 / 3) + 4096)
	}
	hello := frame{Type: "hello", DeviceID: m.opts.Identity.ID, BootID: m.opts.BootID, ConnectionID: rand.Text(), Caps: []string{capPathQuality, capDualSlot}}
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
	dual := slices.Contains(remote.Caps, capDualSlot)
	// Hellos arrive in the order the peer ran, so a handshake of an older run that finishes late is stale.
	p.mu.Lock()
	p.latestBoot = remote.BootID
	p.mu.Unlock()
	newCarrier := func(commit frame, cancel context.CancelFunc) *carrier {
		c := &carrier{conn: conn, epoch: commit.Epoch, id: commit.ConnectionID, path: path, cancel: cancel, outgoing: outgoing, started: time.Now(), peerQuality: slices.Contains(remote.Caps, capPathQuality), done: make(chan struct{})}
		c.busyAt = c.started
		return c
	}
	coordinator := m.opts.Identity.ID < p.id
	var commit frame
	if coordinator {
		p.mu.Lock()
		if err := p.admitLocked(outgoing, replace, dual); err != nil {
			p.mu.Unlock()
			return err
		}
		p.epoch++
		commit = frame{Type: "commit", Epoch: p.epoch, ConnectionID: hello.ConnectionID, BootID: m.opts.BootID}
		// Reserve the slot through its confirmation, blocking concurrent candidates for it.
		carrierCtx, cancel := context.WithCancel(m.ctx)
		c := newCarrier(commit, cancel)
		p.cars = append(p.cars, c)
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
			p.removeLocked(c)
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
	if err := p.admitLocked(outgoing, replace, dual); err != nil {
		p.mu.Unlock()
		return err
	}
	// Each direction is numbered in order by the assigning end, but two handshakes can finish out of order.
	if p.boot == remote.BootID && commit.Epoch <= p.lastEpoch[dirIndex(outgoing)] {
		p.mu.Unlock()
		return ErrUnavailable
	}
	carrierCtx, cancel := context.WithCancel(m.ctx)
	c := newCarrier(commit, cancel)
	p.cars = append(p.cars, c)
	p.epoch = max(p.epoch, commit.Epoch)
	p.lastEpoch[dirIndex(outgoing)] = commit.Epoch
	p.mu.Unlock()
	if err := writeFrame(ctx, conn, frame{Type: "committed", Epoch: commit.Epoch, ConnectionID: commit.ConnectionID}); err != nil {
		cancel()
		p.mu.Lock()
		p.removeLocked(c)
		p.mu.Unlock()
		return err
	}
	return m.activate(p, c, carrierCtx, remote.BootID)
}

func (m *Manager) activate(p *peerLink, c *carrier, ctx context.Context, boot string) error {
	p.mu.Lock()
	drop := func(err error) error {
		c.cancel()
		_ = c.conn.CloseNow()
		p.removeLocked(c)
		p.signalLocked()
		p.mu.Unlock()
		return err
	}
	if !p.disconnected.IsZero() && time.Since(p.disconnected) >= m.opts.RecoveryWindow {
		for _, l := range p.sessions {
			closeLogical(l, ErrResumeExpired)
		}
		for _, l := range p.pending {
			closeLogical(l, ErrResumeExpired)
		}
		return drop(ErrResumeExpired)
	}
	generation, enabled := m.opts.Directory.Provider(c.path.Kind)
	_, trusted := m.opts.Trust.Find(p.id)
	if p.closed || !p.hasLocked(c) || m.ctx.Err() != nil || m.stopping.Load() || !enabled || generation != c.path.ProviderGeneration || m.rank(c.path.Kind) < 0 || !trusted || m.opts.Directory.IdentityConflict(p.id, time.Now()) {
		return drop(ErrUnavailable)
	}
	if p.latestBoot != boot {
		return drop(ErrUnavailable)
	}
	if p.boot != "" && p.boot != boot {
		for _, l := range p.sessions {
			closeLogical(l, ErrRestarted)
		}
		for _, l := range p.pending {
			closeLogical(l, ErrRestarted)
		}
		// Carriers of the previous run of the peer are dead ends.
		for _, o := range p.cars {
			if o != c {
				o.cancel()
				_ = o.conn.CloseNow()
			}
		}
	}
	// A newer carrier in the same direction takes over: the old one stays open only while its sessions move.
	var switched *pathSwitch
	for _, o := range p.cars {
		if o != c && o.committed && o.outgoing == c.outgoing && !o.draining {
			o.draining, o.drainSince = true, time.Now()
			if h := p.handoff; h != nil && h.from == o {
				p.handoff = nil
				if o.path.Kind != c.path.Kind {
					switched = &pathSwitch{id: p.id, from: o.path.Kind, to: c.path.Kind, reason: h.reason}
				}
			}
		}
	}
	p.boot = boot
	c.committed = true
	c.busyAt = time.Now()
	p.disconnected = time.Time{}
	p.signalLocked()
	if primary := p.primaryLocked(); primary == c {
		m.activity(Activity{DeviceID: p.id, Path: c.path, Epoch: c.epoch, State: "online", Outgoing: c.outgoing})
	}
	p.workers.Go(func() { m.serveCarrier(ctx, p, c) })
	p.mu.Unlock()
	if switched != nil && m.opts.OnPathSwitch != nil {
		m.opts.OnPathSwitch(switched.id, switched.from, switched.to, switched.reason)
	}
	return nil
}

func (m *Manager) serveCarrier(ctx context.Context, p *peerLink, c *carrier) {
	defer close(c.done)
	defer func() {
		c.cancel()
		_ = c.conn.CloseNow()
		p.mu.Lock()
		if p.hasLocked(c) {
			wasPrimary := p.primaryLocked() == c
			p.removeLocked(c)
			now := time.Now()
			for _, l := range p.sessions {
				if l.attached == c {
					l.attached = nil
					if l.incoming && l.detachedAt.IsZero() {
						l.detachedAt = now
					}
				}
			}
			for _, l := range p.pending {
				if l.openOn == c {
					l.openOn = nil
				}
			}
			if len(p.cars) == 0 {
				p.disconnected = now
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
			} else if next := p.primaryLocked(); wasPrimary && next != nil {
				m.activity(Activity{DeviceID: p.id, Path: next.path, Epoch: next.epoch, State: next.state(), Outgoing: next.outgoing})
			}
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
		for _, c := range p.cars {
			if c.committed {
				active = append(active, c)
			}
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
	// Closing the socket while the peer's frames are still unread makes the kernel reset the connection,
	// which can discard the notice. The peer hangs up on receiving it, so wait for that.
	for _, c := range active {
		select {
		case <-c.done:
		case <-ctx.Done():
		}
	}
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
		for _, c := range p.cars {
			c.cancel()
			_ = c.conn.CloseNow()
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
