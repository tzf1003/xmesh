package config

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"reflect"
	"sync"
)

// Store reloads validated configuration at the next call, retaining the last
// successful generation on failure (CON-SEC-06).
type Store struct {
	mu         sync.Mutex
	path       string
	current    *Config
	digest     [32]byte
	failed     [32]byte
	readFailed bool
	restart    bool
	failures   uint64
	failing    bool // the file on disk is not the generation in effect because it failed to load
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
	return &Store{path: path, current: c, digest: sha256.Sum256(b)}, nil
}
func Static(c *Config) *Store { return &Store{current: c} }
func (s *Store) Get() *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return s.current
	}
	b, e := os.ReadFile(s.path)
	if e != nil {
		if !s.readFailed {
			s.failures++
			slog.Warn("policy_reload_failed")
		}
		s.readFailed = true
		s.failing = true
		return s.current
	}
	s.readFailed = false
	digest := sha256.Sum256(b)
	if digest == s.digest {
		// The file is the generation in effect again, so the earlier failure is over.
		s.failing = false
		return s.current
	}
	if digest == s.failed {
		s.failing = true
		return s.current
	}
	c, e := Parse(b)
	if e != nil {
		s.failed = digest
		s.failures++
		s.failing = true
		slog.Warn("policy_reload_failed")
		return s.current
	}
	s.failing = false
	// Node, network and listeners use the startup generation until restart.
	s.restart = !reflect.DeepEqual(c.Node, s.current.Node) || !reflect.DeepEqual(c.Mesh, s.current.Mesh) || c.Gateway.Listen != s.current.Gateway.Listen || !reflect.DeepEqual(c.Ingress, s.current.Ingress) || c.Agent.Policy.WindowsAppContainer != s.current.Agent.Policy.WindowsAppContainer
	c.Node = s.current.Node
	c.Mesh = s.current.Mesh
	c.Gateway.Listen = s.current.Gateway.Listen
	c.Ingress = s.current.Ingress
	c.Agent.Policy.WindowsAppContainer = s.current.Agent.Policy.WindowsAppContainer
	// The Gateway reads gateway.expose_backends once at startup.
	if c.Gateway.ExposeBackends != s.current.Gateway.ExposeBackends {
		s.restart = true
		c.Gateway.ExposeBackends = s.current.Gateway.ExposeBackends
	}
	s.current = c
	s.digest = digest
	return c
}

func (s *Store) State() (restart bool, failures uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restart, s.failures
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
