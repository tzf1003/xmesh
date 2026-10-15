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
type Principal struct {
	Node        string
	Tags        []string
	OAuthClient string
}

func FromPeer(node string, tags []string, oauthClient string) (Principal, error) {
	p := Principal{Node: node, Tags: tags}
	if !Valid(node + "/init") {
		return Principal{}, toolerr.New("unauthenticated", "对端身份无效")
	}
	for _, tag := range tags {
		if tag == "tag:ingress" {
			if oauthClient == "" {
				return Principal{}, toolerr.New("unauthenticated", "入口会话缺少 OAuth client 身份")
			}
			p.OAuthClient = oauthClient
			break
		}
	}
	return p, nil
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
