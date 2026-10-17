package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"
)

// RestartPaths are the configuration sections a running process reads only at
// start. A change to them is written to the file and reported through
// restart_required; the running values stay until the next start. Every other
// section takes effect in the next generation (docs/contracts/config.md).
var RestartPaths = []string{"/node", "/mesh", "/gateway/listen", "/ingress", "/audit", "/agent/policy/windows_appcontainer"}

// NeedsRestart reports whether a change at the JSON Pointer ptr, or at a
// section nested below it, falls under RestartPaths. A pointer above a restart
// section, such as an object added whole, counts too.
func NeedsRestart(ptr string) bool {
	for _, p := range RestartPaths {
		if within(ptr, p) || within(p, ptr) {
			return true
		}
	}
	return false
}

// within reports whether path is prefix itself or lies below it.
func within(path, prefix string) bool {
	return path == prefix || (len(path) > len(prefix) && path[:len(prefix)] == prefix && path[len(prefix)] == '/')
}

// Store holds the configuration generation in effect. The file is re-read on
// every Get and Refresh; a valid edit becomes the next generation, and a file
// that fails to load keeps the last good one (CON-SEC-06). Subscribers hear
// about each generation that takes effect.
type Store struct {
	// notify serializes adoption and subscriber calls so they arrive in order.
	notify  sync.Mutex
	mu      sync.Mutex
	path    string
	startup *Config
	current *Config
	// digest is the file the generation in effect corresponds to. While a restart is pending
	// it stays at the last file that needed none, so Digest differs from the file on disk.
	digest [32]byte
	// seen is the last file that loaded, adopted or not; it decides whether a read is new.
	seen        [32]byte
	failed      [32]byte
	readFailed  bool
	restart     []string
	failures    uint64
	failing     bool // the file on disk is not the generation in effect because it failed to load
	subscribers map[uint64]func(*Config)
	nextID      uint64
}

func NewStore(path string) (*Store, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	c, e := Parse(b)
	if e != nil {
		return nil, e
	}
	digest := sha256.Sum256(b)
	return &Store{path: path, startup: c, current: c, digest: digest, seen: digest}, nil
}
func Static(c *Config) *Store { return &Store{startup: c, current: c} }

// Get returns the generation in effect after re-reading the file.
func (s *Store) Get() *Config {
	c, _ := s.reload()
	return c
}

// Refresh re-reads the file now and reports whether a new generation took effect.
// A file that fails to load, or that is already in effect, reports false.
func (s *Store) Refresh() bool {
	_, adopted := s.reload()
	return adopted
}

// Watch re-reads the file every interval until ctx ends, so a change reaches
// subscribers without waiting for a request. It returns at once for a static store.
func (s *Store) Watch(ctx context.Context, every time.Duration) {
	if s.path == "" {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Refresh()
		}
	}
}

// Subscribe calls fn with each generation that takes effect, in order. fn runs on
// the goroutine that adopted the generation, before that call returns, so it must
// not call Get, Refresh or Subscribe. The returned function unsubscribes.
func (s *Store) Subscribe(fn func(*Config)) (unsubscribe func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subscribers == nil {
		s.subscribers = map[uint64]func(*Config){}
	}
	id := s.nextID
	s.nextID++
	s.subscribers[id] = fn
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subscribers, id)
	}
}

func (s *Store) reload() (*Config, bool) {
	s.notify.Lock()
	defer s.notify.Unlock()
	c, adopted := s.load()
	if adopted {
		s.mu.Lock()
		ids := slices.Sorted(maps.Keys(s.subscribers))
		fns := make([]func(*Config), 0, len(ids))
		for _, id := range ids {
			fns = append(fns, s.subscribers[id])
		}
		s.mu.Unlock()
		for _, fn := range fns {
			fn(c)
		}
	}
	return c, adopted
}

func (s *Store) load() (*Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return s.current, false
	}
	b, e := os.ReadFile(s.path)
	if e != nil {
		if !s.readFailed {
			s.failures++
			slog.Warn("policy_reload_failed")
		}
		s.readFailed = true
		s.failing = true
		return s.current, false
	}
	s.readFailed = false
	digest := sha256.Sum256(b)
	if digest == s.seen {
		// The file is the one already loaded, so an earlier failure is over.
		s.failing = false
		return s.current, false
	}
	if digest == s.failed {
		s.failing = true
		return s.current, false
	}
	c, e := Parse(b)
	if e != nil {
		s.failed = digest
		s.failures++
		s.failing = true
		slog.Warn("policy_reload_failed")
		return s.current, false
	}
	s.failing = false
	// Sections in RestartPaths keep the startup values until restart; the file's
	// values are reported as pending.
	s.restart = restartChanged(s.startup, c)
	c.Node = s.startup.Node
	c.Mesh = s.startup.Mesh
	c.Gateway.Listen = s.startup.Gateway.Listen
	c.Ingress = s.startup.Ingress
	c.Audit = s.startup.Audit
	c.Agent.Policy.WindowsAppContainer = s.startup.Agent.Policy.WindowsAppContainer
	s.current = c
	s.seen = digest
	if len(s.restart) == 0 {
		s.digest = digest
	}
	return c, true
}

// restartChanged lists the RestartPaths whose value in next differs from the
// startup generation.
func restartChanged(startup, next *Config) []string {
	var out []string
	add := func(ptr string, changed bool) {
		if changed {
			out = append(out, ptr)
		}
	}
	add("/node", !reflect.DeepEqual(startup.Node, next.Node))
	add("/mesh", !reflect.DeepEqual(startup.Mesh, next.Mesh))
	add("/gateway/listen", startup.Gateway.Listen != next.Gateway.Listen)
	add("/ingress", !reflect.DeepEqual(startup.Ingress, next.Ingress))
	add("/audit", !reflect.DeepEqual(startup.Audit, next.Audit))
	add("/agent/policy/windows_appcontainer", startup.Agent.Policy.WindowsAppContainer != next.Agent.Policy.WindowsAppContainer)
	return out
}

func (s *Store) State() (restart bool, failures uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.restart) > 0, s.failures
}

// RestartPending lists the RestartPaths whose value in the file differs from the
// running one. It is empty when the file needs no restart.
func (s *Store) RestartPending() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.restart...)
}

// Failing reports whether the configuration file on disk currently fails to
// load, so the last good generation is still in effect. Unlike the failure count
// it clears once the file loads again, whether with new content or restored to
// the generation in effect.
func (s *Store) Failing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failing
}

// Path is the configuration file this store reloads, or "" for a static store.
func (s *Store) Path() string { return s.path }

// Digest is the SHA-256 (hex) of the file bytes behind the generation now in
// effect. It differs from the file's own digest while an edit is pending a
// restart or was rejected on reload. Static stores report "".
func (s *Store) Digest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return ""
	}
	return hex.EncodeToString(s.digest[:])
}
