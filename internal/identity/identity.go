// Package identity holds transport-derived caller identities (CTR-ID-01).
package identity

import (
	"github.com/tzf1003/xmesh/internal/toolerr"
	"regexp"
	"strings"
	"sync"
)

type Caller struct {
	ID          string
	Tags        []string
	OAuthClient string
}

var part = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func Valid(id string) bool {
	a, b, ok := strings.Cut(id, "/")
	return ok && part.MatchString(a) && part.MatchString(b)
}
func Node(id string) string { n, _, _ := strings.Cut(id, "/"); return n }

type Registry struct {
	mu      sync.RWMutex
	callers map[string]Caller
}

func NewRegistry() *Registry { return &Registry{callers: map[string]Caller{}} }
func (r *Registry) Add(c Caller) error {
	if !Valid(c.ID) {
		return toolerr.Args("caller_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callers[c.ID] = c
	return nil
}
func (r *Registry) Get(id string) (Caller, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.callers[id]
	return c, ok
}
func (r *Registry) Remove(id string) { r.mu.Lock(); defer r.mu.Unlock(); delete(r.callers, id) }
