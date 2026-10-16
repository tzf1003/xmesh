package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"time"
)

// leaseBeat is the Gateway's heartbeat for an exclusive backend lease that a
// session acquired through this view (CTR-LEASE-01, "谁来心跳").
type leaseBeat struct {
	cancel context.CancelFunc
}

func leaseKey(device, backend string) string { return device + "\x00" + backend }

// keepLease starts renewing a lease that backend.acquire just granted on
// device through this view's own downstream connection. The renewal runs at
// half the granted TTL until the upstream session ends, the lease is released,
// or the Agent no longer recognises it.
func (v *view) keepLease(device string, d *downstream, args map[string]json.RawMessage, out *mcp.CallToolResult) {
	var backend, ttlText string
	if json.Unmarshal(args["name"], &backend) != nil || json.Unmarshal(args["ttl"], &ttlText) != nil || backend == "" {
		return
	}
	ttl, e := time.ParseDuration(ttlText)
	if e != nil || ttl <= 0 || d == nil {
		return
	}
	b, e := json.Marshal(out.StructuredContent)
	if e != nil {
		return
	}
	var granted struct {
		Token json.RawMessage `json:"token"`
	}
	if json.Unmarshal(b, &granted) != nil || len(granted.Token) == 0 {
		return
	}
	key := leaseKey(device, backend)
	v.mu.Lock()
	if v.closed || v.down[device] != d || d.dead {
		v.mu.Unlock()
		return
	}
	if old := v.leases[key]; old != nil {
		old.cancel()
	}
	ctx, cancel := context.WithCancel(v.ctx)
	beat := &leaseBeat{cancel: cancel}
	v.leases[key] = beat
	v.workers.Add(1)
	v.mu.Unlock()
	go func() {
		defer v.workers.Done()
		defer cancel()
		v.heartbeat(ctx, device, d, backend, granted.Token, ttlText, ttl)
		v.mu.Lock()
		if v.leases[key] == beat {
			delete(v.leases, key)
		}
		v.mu.Unlock()
	}()
}

// dropLease stops the heartbeat after the session released the lease itself.
func (v *view) dropLease(device string, args map[string]json.RawMessage) {
	var backend string
	if json.Unmarshal(args["name"], &backend) != nil {
		return
	}
	v.mu.Lock()
	beat := v.leases[leaseKey(device, backend)]
	v.mu.Unlock()
	if beat != nil {
		beat.cancel()
	}
}

func (v *view) heartbeat(ctx context.Context, device string, d *downstream, backend string, token json.RawMessage, ttlText string, ttl time.Duration) {
	expires := time.Now().Add(ttl)
	wait := ttl / 2
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		v.mu.Lock()
		alive := v.down[device] == d && !d.dead
		v.mu.Unlock()
		if !alive || time.Now().After(expires) {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, max(time.Until(expires), time.Second))
		out, e := d.bridge.Call(callCtx, "backend.renew", map[string]any{"name": backend, "_lease_token": token, "ttl": ttlText}, mcpmux.Progress{})
		cancel()
		switch {
		case e == nil && out != nil && !out.IsError:
			expires = time.Now().Add(ttl)
			wait = ttl / 2
		case e == nil:
			// resource_busy: the Agent no longer holds this token for us.
			return
		default:
			// Transport trouble: retry sooner, but never past the old expiry.
			wait = max(ttl/8, 10*time.Millisecond)
		}
		timer.Reset(wait)
	}
}
