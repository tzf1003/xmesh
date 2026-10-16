// Package lease owns in-memory exclusive leases and monotonic fencing tokens.
package lease

import (
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"math"
	"strconv"
	"sync"
	"time"
)

type Lease struct {
	Resource string    `json:"resource"`
	Owner    string    `json:"owner"`
	Token    uint64    `json:"token"`
	Expires  time.Time `json:"expires"`
}
type resource struct {
	counter       uint64
	lease         *Lease
	releasedOwner string
	releasedToken uint64
	exhausted     bool
}
type Manager struct {
	mu        sync.Mutex
	resources map[string]*resource
	now       func() time.Time
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}

func New() *Manager {
	m := &Manager{resources: map[string]*resource{}, now: time.Now, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-tick.C:
				m.Sweep()
			}
		}
	}()
	return m
}
func (m *Manager) Close() { m.once.Do(func() { close(m.stop); <-m.done }) }
func (m *Manager) entry(name string) *resource {
	r := m.resources[name]
	if r == nil {
		r = &resource{}
		m.resources[name] = r
	}
	if r.lease != nil && !r.lease.Expires.After(m.now()) {
		r.lease = nil
	}
	return r
}
func (m *Manager) next(r *resource) error {
	if r.exhausted || r.counter == math.MaxUint64 {
		r.exhausted = true
		return toolerr.New("internal", "租约序号已耗尽", "op", "lease.fence")
	}
	r.counter++
	return nil
}
func busy(name string, r *resource, now time.Time, reason string) error {
	owner := ""
	seconds := int64(0)
	if r.lease != nil {
		owner = r.lease.Owner
		seconds = max(int64(0), int64(r.lease.Expires.Sub(now).Seconds()))
	}
	return toolerr.New("resource_busy", "后端租约不可用", "resource", name, "held_by", owner, "expires_in_s", seconds, "reason", reason)
}
func (m *Manager) Acquire(name, owner string, ttl, maxTTL time.Duration) (Lease, error) {
	if ttl <= 0 || ttl > maxTTL {
		return Lease{}, toolerr.Args("ttl")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.entry(name)
	if r.lease != nil {
		if r.lease.Owner != owner {
			return Lease{}, busy(name, r, m.now(), "held")
		}
		r.lease.Expires = m.now().Add(ttl)
		return *r.lease, nil
	}
	if e := m.next(r); e != nil {
		return Lease{}, e
	}
	r.lease = &Lease{name, owner, r.counter, m.now().Add(ttl)}
	return *r.lease, nil
}
func (m *Manager) validate(name, owner string, token uint64) (*resource, error) {
	r := m.entry(name)
	if r.lease == nil || r.lease.Token != token {
		return nil, busy(name, r, m.now(), "token_stale")
	}
	if r.lease.Owner != owner {
		return nil, busy(name, r, m.now(), "token_mismatch")
	}
	return r, nil
}
func (m *Manager) Validate(name, owner string, token uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, e := m.validate(name, owner, token)
	return e
}
func (m *Manager) Renew(name, owner string, token uint64, ttl, maxTTL time.Duration) (Lease, error) {
	if ttl <= 0 || ttl > maxTTL {
		return Lease{}, toolerr.Args("ttl")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, e := m.validate(name, owner, token)
	if e != nil {
		return Lease{}, e
	}
	r.lease.Expires = m.now().Add(ttl)
	return *r.lease, nil
}
func (m *Manager) Release(name, owner string, token uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.entry(name)
	if r.lease == nil && r.releasedOwner == owner && r.releasedToken == token {
		return nil
	}
	r, e := m.validate(name, owner, token)
	if e != nil {
		return e
	}
	r.releasedOwner = owner
	r.releasedToken = token
	r.lease = nil
	return nil
}
func (m *Manager) Invalidate(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.entry(name)
	r.lease = nil
	r.releasedOwner = ""
	r.releasedToken = 0
	return m.next(r)
}
func (m *Manager) Owner(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.entry(name)
	if r.lease == nil {
		return ""
	}
	return r.lease.Owner
}
func (m *Manager) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name := range m.resources {
		m.entry(name)
	}
}
func (m *Manager) ReleaseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resources {
		r.lease = nil
		_ = m.next(r)
	}
}
func ParseToken(raw json.RawMessage) (uint64, error) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = string(raw)
	}
	token, e := strconv.ParseUint(text, 10, 64)
	if e != nil || token == 0 {
		return 0, toolerr.Args("_lease_token")
	}
	return token, nil
}
