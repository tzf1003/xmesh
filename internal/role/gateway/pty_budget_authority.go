package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// authorityLease is a cross-host admission lease. deadline is computed from the
// instant before the acquire request was sent, so it never outlives the
// authority's own expiry.
type authorityLease struct {
	peer     mesh.Peer
	token    uint64
	deadline time.Time
	pending  map[string]string
	stale    map[string]string
	outcome  string
}

func (v *view) authorityCall(ctx context.Context, peer mesh.Peer, request pty.AuthorityRequest) (pty.AuthorityReply, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var reply pty.AuthorityReply
	bridge := v.readyBridge(ctx, peer)
	if bridge == nil {
		return reply, budgetUnavailable()
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return reply, err
	}
	uri := pty.BudgetAuthorityURI + "?q=" + base64.RawURLEncoding.EncodeToString(raw)
	result, err := bridge.Session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return reply, err
	}
	if result == nil || len(result.Contents) != 1 || result.Contents[0] == nil || result.Contents[0].URI != uri || len(result.Contents[0].Text) > 1<<20 {
		return reply, budgetUnavailable()
	}
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &reply); err != nil {
		return reply, err
	}
	return reply, nil
}

// acquireAuthority waits for the cross-host lease. It fails closed when the
// authority is offline, unreachable or does not answer within b.wait.
func (b *PTYBudget) acquireAuthority(ctx context.Context, v *view, peers map[string]mesh.Peer) (*authorityLease, error) {
	var peer mesh.Peer
	found := false
	for _, candidate := range peers {
		if candidate.Name == b.authority {
			peer, found = candidate, true
		}
	}
	if !found {
		return nil, budgetUnavailable()
	}
	waitUntil := time.Now().Add(b.wait)
	for {
		started := time.Now()
		request := pty.AuthorityRequest{Op: "acquire", Owner: b.owner, Settle: b.unsettled}
		reply, err := v.authorityCall(ctx, peer, request)
		if err != nil {
			return nil, budgetUnavailable()
		}
		ttl := time.Duration(reply.TTLMs) * time.Millisecond
		if reply.OK && reply.Token != 0 && ttl >= time.Second/2 {
			// The previous lease is settled once the authority has accepted it.
			b.unsettled = nil
			return &authorityLease{peer: peer, token: reply.Token, deadline: started.Add(ttl - pty.AuthorityMarginFor(ttl)), pending: reply.Pending, stale: map[string]string{}, outcome: "ok"}, nil
		}
		if !reply.Busy {
			return nil, budgetUnavailable()
		}
		if time.Now().After(waitUntil) {
			return nil, authorityBusy()
		}
		pause := min(max(time.Duration(reply.RetryMs)*time.Millisecond, 20*time.Millisecond), 250*time.Millisecond)
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return nil, budgetUnavailable()
		}
	}
}

func (b *PTYBudget) markAuthority(ctx context.Context, v *view, lease *authorityLease, target mesh.Peer, epoch string) error {
	reply, err := v.authorityCall(ctx, lease.peer, pty.AuthorityRequest{Op: "mark", Owner: b.owner, Token: lease.token, Peer: target.ID, Epoch: epoch})
	if err != nil || !reply.OK || time.Now().After(lease.deadline) {
		return budgetUnavailable()
	}
	return nil
}

// releaseAuthority reports the outcome of the lease. A release that cannot be
// delivered after an "ok" outcome is retried with the next acquire; an
// "uncertain" outcome intentionally leaves the authority's pending record.
func (b *PTYBudget) releaseAuthority(v *view, lease *authorityLease) {
	for attempt := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		reply, err := v.authorityCall(ctx, lease.peer, pty.AuthorityRequest{Op: "release", Owner: b.owner, Token: lease.token, Outcome: lease.outcome, Stale: lease.stale})
		cancel()
		if err == nil {
			if !reply.OK {
				slog.Warn("pty_budget_authority_release_rejected")
			}
			return
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	if lease.outcome == "ok" {
		b.unsettled = &pty.AuthoritySettle{Token: lease.token, Outcome: "ok"}
	}
	slog.Error("pty_budget_authority_release_failed")
}

func authorityBusy() error {
	return toolerr.New("policy_denied", "无法确认入口 PTY 预算", "rule", "quota.unavailable", "reason", "跨入口主机的预算协调不可用或被占用，请稍后重试")
}
