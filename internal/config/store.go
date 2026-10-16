package config

import (
	"crypto/sha256"
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
		return s.current
	}
	s.readFailed = false
	digest := sha256.Sum256(b)
	if digest == s.digest || digest == s.failed {
		return s.current
	}
	c, e := Parse(b)
	if e != nil {
		s.failed = digest
		s.failures++
		slog.Warn("policy_reload_failed")
		return s.current
	}
	// Node, network and listeners use the startup generation until restart.
	s.restart = !reflect.DeepEqual(c.Node, s.current.Node) || !reflect.DeepEqual(c.Mesh, s.current.Mesh) || c.Gateway.Listen != s.current.Gateway.Listen || !reflect.DeepEqual(c.Ingress, s.current.Ingress)
	c.Node = s.current.Node
	c.Mesh = s.current.Mesh
	c.Gateway.Listen = s.current.Gateway.Listen
	c.Ingress = s.current.Ingress
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
