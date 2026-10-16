package pty

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// BudgetAuthorityURI is the control resource template an ingress uses to take
// the cross-host admission lease hosted by one explicitly chosen Agent.
const BudgetAuthorityURI = "xmesh://control/pty-budget-authority"

const (
	// AuthorityTTL bounds how long a silent holder blocks other ingress hosts.
	AuthorityTTL = 30 * time.Second
	// AuthorityMargin is kept free at the end of a lease so the holder stops
	// issuing calls before the authority can hand the lease to someone else.
	AuthorityMargin = 5 * time.Second
)

// AuthorityRequest is the base64url-encoded JSON carried in the "q" query of
// BudgetAuthorityURI. Owner is a nonce chosen by the ingress process; the
// Agent prefixes the authenticated node identity, so owners of different
// ingress hosts cannot collide or be impersonated.
type AuthorityRequest struct {
	Op      string `json:"op"` // acquire, mark, release
	Owner   string `json:"owner"`
	Token   uint64 `json:"token,omitempty"`
	Peer    string `json:"peer,omitempty"`
	Epoch   string `json:"epoch,omitempty"`
	Outcome string `json:"outcome,omitempty"` // ok or uncertain, for release
	// Settle reports the outcome of this owner's previous lease whose release
	// could not be delivered, so a transient network error is not mistaken
	// for an uncertain terminal call.
	Settle *AuthoritySettle `json:"settle,omitempty"`
	// Stale lists pending records whose target Agent has since restarted.
	Stale map[string]string `json:"stale,omitempty"`
}

type AuthoritySettle struct {
	Token   uint64 `json:"token"`
	Outcome string `json:"outcome"`
}

// AuthorityReply is the authority's answer. Busy means the lease is held (or
// the authority is still settling after a restart) and RetryMs says when a
// retry is useful.
type AuthorityReply struct {
	OK      bool              `json:"ok"`
	Busy    bool              `json:"busy,omitempty"`
	RetryMs int64             `json:"retry_ms,omitempty"`
	Reason  string            `json:"reason,omitempty"`
	Epoch   string            `json:"epoch,omitempty"`
	Token   uint64            `json:"token,omitempty"`
	TTLMs   int64             `json:"ttl_ms,omitempty"`
	Pending map[string]string `json:"pending,omitempty"` // peer id -> Agent epoch
}

type authorityHold struct {
	owner   string
	token   uint64
	expires time.Time
}

type authorityMark struct {
	epoch string
	owner string
	token uint64
}

// BudgetAuthority is a single in-memory mutex with fencing tokens plus the
// uncertain-call records that every ingress must honour before admitting a
// terminal. Everything is lost on restart, so grants are refused for one TTL
// after the process starts: any lease handed out by the previous process has
// expired by then and its holder has stopped issuing calls.
type BudgetAuthority struct {
	mu        sync.Mutex
	now       func() time.Time
	epoch     string
	ttl       time.Duration
	ready     time.Time
	counter   uint64
	lastOwner string
	held      *authorityHold
	pending   map[string]authorityMark
}

func NewBudgetAuthority() *BudgetAuthority { return newBudgetAuthority(time.Now, AuthorityTTL) }

// NewBudgetAuthorityTTL is for tests that need short leases; the settling
// period after start equals the lease TTL.
func NewBudgetAuthorityTTL(ttl time.Duration) *BudgetAuthority {
	return newBudgetAuthority(time.Now, ttl)
}

// AuthorityMarginFor is the part of a lease the holder never uses.
func AuthorityMarginFor(ttl time.Duration) time.Duration { return min(AuthorityMargin, ttl/6) }

func newBudgetAuthority(now func() time.Time, ttl time.Duration) *BudgetAuthority {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return &BudgetAuthority{now: now, epoch: hex.EncodeToString(raw[:]), ttl: ttl, ready: now().Add(ttl), pending: map[string]authorityMark{}}
}

func (a *BudgetAuthority) expire(now time.Time) {
	if a.held != nil && !a.held.expires.After(now) {
		a.held = nil
	}
}

func (a *BudgetAuthority) settle(owner string, s *AuthoritySettle) {
	if s == nil || s.Outcome != "ok" {
		return
	}
	for peer, mark := range a.pending {
		if mark.owner == owner && mark.token == s.Token {
			delete(a.pending, peer)
		}
	}
}

func (a *BudgetAuthority) pendingCopy() map[string]string {
	out := make(map[string]string, len(a.pending))
	for peer, mark := range a.pending {
		out[peer] = mark.epoch
	}
	return out
}

// Handle executes one request; owner must already be qualified by the caller's
// authenticated node identity.
func (a *BudgetAuthority) Handle(req AuthorityRequest, owner string) (AuthorityReply, error) {
	if owner == "" || len(owner) > 256 {
		return AuthorityReply{}, fmt.Errorf("invalid owner")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.expire(now)
	switch req.Op {
	case "acquire":
		if now.Before(a.ready) {
			return AuthorityReply{Busy: true, Reason: "settling", RetryMs: a.ready.Sub(now).Milliseconds() + 1, Epoch: a.epoch}, nil
		}
		a.settle(owner, req.Settle)
		for peer, epoch := range req.Stale {
			if mark, ok := a.pending[peer]; ok && mark.epoch == epoch {
				delete(a.pending, peer)
			}
		}
		if a.held != nil {
			return AuthorityReply{Busy: true, Reason: "held", RetryMs: min(a.held.expires.Sub(now).Milliseconds()+1, 200), Epoch: a.epoch}, nil
		}
		a.counter++
		a.held = &authorityHold{owner: owner, token: a.counter, expires: now.Add(a.ttl)}
		a.lastOwner = owner
		return AuthorityReply{OK: true, Epoch: a.epoch, Token: a.held.token, TTLMs: a.ttl.Milliseconds(), Pending: a.pendingCopy()}, nil
	case "mark":
		if a.held == nil || a.held.owner != owner || a.held.token != req.Token {
			return AuthorityReply{Reason: "lease_lost", Epoch: a.epoch}, nil
		}
		if req.Peer == "" || req.Epoch == "" {
			return AuthorityReply{}, fmt.Errorf("invalid mark")
		}
		a.pending[req.Peer] = authorityMark{epoch: req.Epoch, owner: owner, token: req.Token}
		return AuthorityReply{OK: true, Epoch: a.epoch}, nil
	case "release":
		// A lease that expired before its release arrived is still finished
		// when nobody was granted the mutex since, so nothing interleaved.
		lapsed := a.held == nil && a.counter == req.Token && a.lastOwner == owner
		if !lapsed && (a.held == nil || a.held.owner != owner || a.held.token != req.Token) {
			return AuthorityReply{Reason: "lease_lost", Epoch: a.epoch}, nil
		}
		a.settle(owner, &AuthoritySettle{Token: req.Token, Outcome: req.Outcome})
		for peer, epoch := range req.Stale {
			if mark, ok := a.pending[peer]; ok && mark.epoch == epoch {
				delete(a.pending, peer)
			}
		}
		a.held = nil
		return AuthorityReply{OK: true, Epoch: a.epoch}, nil
	}
	return AuthorityReply{}, fmt.Errorf("unknown op")
}
