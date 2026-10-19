package connectivity

import (
	"context"
	"reflect"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/device"
)

// Settle waits for the submitted network configuration to become operational.
func (n *Node) Settle(ctx context.Context) []v2.ApplyError {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		n.mu.Lock()
		cfg := n.cfg
		out := []v2.ApplyError{}
		pending := false
		for _, kind := range []device.PathKind{device.LAN, device.Tailnet} {
			enabled := cfg.LANEnabled()
			if kind == device.Tailnet {
				enabled = cfg.TailnetEnabled()
			}
			p := n.providers[kind]
			same := p != nil && ((kind == device.LAN && reflect.DeepEqual(p.cfg.LAN, cfg.LAN)) || (kind == device.Tailnet && reflect.DeepEqual(p.cfg.Tailnet, cfg.Tailnet)))
			if !enabled && (p == nil || (same && !p.ready)) {
				continue
			}
			message := "网络路径正在应用配置"
			if same && p.err != "" {
				message = p.err
			} else if !same || !p.ready {
				pending = true
			} else {
				continue
			}
			out = append(out, v2.ApplyError{Section: "mesh." + string(kind), Message: message})
		}
		n.mu.Unlock()
		if !pending || ctx.Err() != nil {
			return out
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}
