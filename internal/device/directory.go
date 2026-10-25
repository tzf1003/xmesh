package device

import (
	"context"
	"crypto/x509"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

type PathKind string

const (
	LAN     PathKind = "lan"
	Tailnet PathKind = "tailnet"
	// Cloudflare reaches a device through its own Cloudflare Tunnel hostname; it needs no inbound address.
	Cloudflare PathKind = "cloudflare"
)

var ErrStalePath = errors.New("连接路径版本已过期")

// Path is an authenticated candidate. Reachability and an active logical connection are
// separate: an expired discovery announcement alone does not end an established session.
type Path struct {
	Kind               PathKind  `json:"kind"`
	Address            string    `json:"address"`
	ProviderGeneration uint64    `json:"provider_generation"`
	BootID             string    `json:"boot_id"`
	VerifiedAt         time.Time `json:"verified_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	Reachable          bool      `json:"reachable"`
	// RTT is the measured time of the probe that produced this observation. It is input to
	// Observe; Snapshot reports the windowed Quality instead.
	RTT     time.Duration `json:"-"`
	Quality Quality       `json:"quality"`
}

type Device struct {
	DeviceID         string   `json:"device_id"`
	Name             string   `json:"name"`
	Alias            string   `json:"alias"`
	Tags             []string `json:"tags"`
	Paths            []Path   `json:"paths"`
	IdentityConflict bool     `json:"identity_conflict"`
}

type pathKey struct {
	kind    PathKind
	address string
}
type providerState struct {
	generation uint64
	enabled    bool
}

// Directory merges verified paths by the public device identity. Each provider owns its
// generation, so callbacks from a stopped provider cannot repopulate a replacement's paths.
type Directory struct {
	mu          sync.Mutex
	trust       *TrustStore
	self        string
	providers   map[PathKind]providerState
	paths       map[string]map[pathKey]Path
	stats       map[string]map[pathKey]*pathStats
	revision    uint64
	subscribers map[uint64]chan uint64
	next        uint64
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewDirectory(ctx context.Context, trust *TrustStore, self string) *Directory {
	ctx, cancel := context.WithCancel(ctx)
	d := &Directory{trust: trust, self: self, providers: map[PathKind]providerState{}, paths: map[string]map[pathKey]Path{}, stats: map[string]map[pathKey]*pathStats{}, subscribers: map[uint64]chan uint64{}, cancel: cancel, done: make(chan struct{})}
	changed, unsubscribe := trust.Subscribe()
	<-changed // The empty directory already reflects the initial trust snapshot.
	go func() {
		defer close(d.done)
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-changed:
				if !ok {
					return
				}
				d.mu.Lock()
				for id := range d.paths {
					if _, ok := trust.Find(id); !ok {
						delete(d.paths, id)
						delete(d.stats, id)
					}
				}
				d.notifyLocked()
				d.mu.Unlock()
			}
		}
	}()
	return d
}

// Valid reports whether k is a path kind this runtime knows.
func (k PathKind) Valid() bool { return k == LAN || k == Tailnet || k == Cloudflare }

func (d *Directory) SetProvider(kind PathKind, generation uint64, enabled bool) error {
	if !kind.Valid() || generation == 0 {
		return ErrStalePath
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return context.Canceled
	}
	previous := d.providers[kind]
	if generation < previous.generation || (generation == previous.generation && enabled != previous.enabled) {
		return ErrStalePath
	}
	if previous.generation == generation && previous.enabled == enabled {
		return nil
	}
	d.providers[kind] = providerState{generation, enabled}
	for _, paths := range d.paths {
		for key := range paths {
			if key.kind == kind {
				delete(paths, key)
			}
		}
	}
	for _, stats := range d.stats {
		for key := range stats {
			if key.kind == kind {
				delete(stats, key)
			}
		}
	}
	d.notifyLocked()
	return nil
}

func (d *Directory) Provider(kind PathKind) (uint64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.providers[kind]
	return p.generation, p.enabled && !d.closed
}

// Observe accepts evidence only after mTLS identity verification, with the exact trust generation
// the probe was authorized against. The boot ID is read over that authenticated connection.
func (d *Directory) Observe(cert *x509.Certificate, trustGeneration uint64, path Path) error {
	trust, err := d.trust.VerifyCertificate(cert, "")
	if err != nil {
		return err
	}
	if trust.Document.DeviceID == d.self || trust.Generation != trustGeneration || path.BootID == "" || path.Address == "" || path.VerifiedAt.IsZero() || !path.ExpiresAt.After(path.VerifiedAt) {
		return ErrStalePath
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return context.Canceled
	}
	provider := d.providers[path.Kind]
	if !provider.enabled || provider.generation != path.ProviderGeneration {
		return ErrStalePath
	}
	// Recheck trust after acquiring the directory lock; snapshots also filter current trust.
	current, ok := d.trust.Find(trust.Document.DeviceID)
	if !ok || current.Generation != trustGeneration {
		return ErrGeneration
	}
	paths := d.paths[trust.Document.DeviceID]
	if paths == nil {
		paths = map[pathKey]Path{}
		d.paths[trust.Document.DeviceID] = paths
	}
	key := pathKey{path.Kind, path.Address}
	if old, ok := paths[key]; ok && path.VerifiedAt.Before(old.VerifiedAt) {
		return ErrStalePath
	}
	path.Reachable = true
	d.recordLocked(trust.Document.DeviceID, key, sample{at: path.VerifiedAt, ok: true, rtt: path.RTT})
	paths[key] = path
	d.notifyLocked()
	return nil
}

// maxStatsPerDevice bounds the addresses one device can accumulate history for (a roaming laptop
// meets many LAN addresses over a long-running process).
const maxStatsPerDevice = 64

// evictStatsLocked frees room for a new path: it drops histories that aged out of the window,
// and if every one is still live, the one probed longest ago.
func evictStatsLocked(byKey map[pathKey]*pathStats, now time.Time) {
	var oldest pathKey
	var oldestAt time.Time
	for key, st := range byKey {
		if st.quality(now).Samples == 0 {
			delete(byKey, key)
			continue
		}
		if at := st.last(); oldestAt.IsZero() || at.Before(oldestAt) {
			oldest, oldestAt = key, at
		}
	}
	if len(byKey) >= maxStatsPerDevice && !oldestAt.IsZero() {
		delete(byKey, oldest)
	}
}

func (d *Directory) recordLocked(id string, key pathKey, x sample) {
	if _, ok := d.trust.Find(id); !ok {
		return
	}
	byKey := d.stats[id]
	if byKey == nil {
		byKey = map[pathKey]*pathStats{}
		d.stats[id] = byKey
	}
	st := byKey[key]
	if st == nil {
		if len(byKey) >= maxStatsPerDevice {
			evictStatsLocked(byKey, x.at)
		}
		st = &pathStats{}
		byKey[key] = st
	}
	st.add(x)
}

// Lost changes only one provider path. It neither revokes the device nor ends its sessions.
func (d *Directory) Lost(id string, kind PathKind, address string, generation uint64) error {
	return d.lost(id, kind, address, generation, time.Time{})
}

// ProbeFailed retires evidence only if a newer probe has not already replaced it.
func (d *Directory) ProbeFailed(id string, kind PathKind, address string, generation uint64, started time.Time) error {
	if started.IsZero() {
		return ErrStalePath
	}
	return d.lost(id, kind, address, generation, started)
}

func (d *Directory) lost(id string, kind PathKind, address string, generation uint64, started time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return context.Canceled
	}
	provider := d.providers[kind]
	if !provider.enabled || provider.generation != generation {
		return ErrStalePath
	}
	key := pathKey{kind, address}
	if path, ok := d.paths[id][key]; ok && !started.IsZero() && path.VerifiedAt.After(started) {
		return ErrStalePath
	}
	if !started.IsZero() {
		d.recordLocked(id, key, sample{at: started})
	}
	if path, ok := d.paths[id][key]; ok && path.Reachable {
		path.Reachable = false
		d.paths[id][key] = path
		d.notifyLocked()
	}
	return nil
}

func (d *Directory) Snapshot(now time.Time) ([]Device, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	trusted := d.trust.Snapshot()
	result := make([]Device, 0, len(trusted))
	for _, trust := range trusted {
		id := trust.Document.DeviceID
		if id == d.self {
			continue
		}
		device := Device{DeviceID: id, Name: trust.Document.Name, Alias: trust.Alias, Tags: slices.Clone(trust.Tags), Paths: []Path{}}
		for _, path := range d.paths[id] {
			path.Reachable = path.Reachable && now.Before(path.ExpiresAt)
			if st := d.stats[id][pathKey{path.Kind, path.Address}]; st != nil {
				path.Quality = st.quality(now)
			}
			device.Paths = append(device.Paths, path)
		}
		// A path whose probes have only failed has no observation, but its loss is exactly what
		// the user needs to see, so report it as unreachable rather than letting it vanish.
		for key, st := range d.stats[id] {
			if _, observed := d.paths[id][key]; observed {
				continue
			}
			q := st.quality(now)
			if q.Samples == 0 {
				delete(d.stats[id], key)
				continue
			}
			device.Paths = append(device.Paths, Path{Kind: key.kind, Address: key.address, Quality: q})
		}
		device.IdentityConflict = identityConflict(d.paths[id], now)
		slices.SortFunc(device.Paths, comparePath)
		result = append(result, device)
	}
	return result, d.revision
}

// IdentityConflict fences links while authenticated paths claim different live processes.
func (d *Directory) IdentityConflict(id string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return identityConflict(d.paths[id], now)
}

func identityConflict(paths map[pathKey]Path, now time.Time) bool {
	boot := ""
	for _, path := range paths {
		if !path.Reachable || !now.Before(path.ExpiresAt) {
			continue
		}
		if boot != "" && boot != path.BootID {
			return true
		}
		boot = path.BootID
	}
	return false
}

func comparePath(a, b Path) int {
	if a.Kind != b.Kind {
		if a.Kind == LAN {
			return -1
		}
		return 1
	}
	if a.Reachable != b.Reachable {
		if a.Reachable {
			return -1
		}
		return 1
	}
	return strings.Compare(a.Address, b.Address)
}

func (d *Directory) Subscribe() (<-chan uint64, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ch := make(chan uint64, 1)
	if d.closed {
		close(ch)
		return ch, func() {}
	}
	d.next++
	id := d.next
	ch <- d.revision
	d.subscribers[id] = ch
	return ch, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if c, ok := d.subscribers[id]; ok {
			delete(d.subscribers, id)
			close(c)
		}
	}
}

func (d *Directory) notifyLocked() {
	if d.closed {
		return
	}
	d.revision++
	for _, ch := range d.subscribers {
		select {
		case <-ch:
		default:
		}
		ch <- d.revision
	}
}

func (d *Directory) Close() {
	d.cancel()
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	for id, ch := range d.subscribers {
		close(ch)
		delete(d.subscribers, id)
	}
}
