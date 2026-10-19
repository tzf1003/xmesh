package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/connectivity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func (h *controlHost) RemoteDrain(ctx context.Context, req v2.DrainRequest) ([]v2.DrainOutcome, error) {
	node, ok := h.d.network.(*connectivity.Node)
	if !ok {
		return nil, fmt.Errorf("请启用设备网络后执行远程排空")
	}
	return remoteDrain(ctx, node, req)
}

type drainSender interface {
	Peers(context.Context) ([]mesh.Peer, error)
	SendDrain(context.Context, mesh.Peer, agent.DrainRequest) error
}

func remoteDrain(ctx context.Context, node drainSender, req v2.DrainRequest) ([]v2.DrainOutcome, error) {
	timeoutMS := req.TimeoutMS
	if timeoutMS == 0 {
		timeoutMS = v2.DefaultDrainTimeoutMS
	}
	if timeoutMS < 0 || timeoutMS > v2.MaxDrainTimeoutMS {
		return nil, fmt.Errorf("设备排空超时无效")
	}
	peers, err := node.Peers(ctx)
	if err != nil {
		return nil, err
	}
	targets := []mesh.Peer{}
	for _, peer := range peers {
		if peer.Online && slices.Contains(peer.Tags, "tag:device") && (req.Scope == "mesh" || req.Device == peer.ID || req.Device == peer.Name) {
			targets = append(targets, peer)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("没有匹配的在线设备")
	}
	slices.SortFunc(targets, func(a, b mesh.Peer) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	result := make([]v2.DrainOutcome, len(targets))
	limit := make(chan struct{}, 8)
	var workers sync.WaitGroup
	for index, peer := range targets {
		workers.Go(func() {
			result[index].Device = peer.Name
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				result[index].Error = ctx.Err().Error()
				return
			}
			defer func() { <-limit }()
			attempt, stop := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
			defer stop()
			if err := node.SendDrain(attempt, peer, agent.DrainRequest{Scope: req.Scope, Device: req.Device, Drain: string(req.Mode)}); err != nil {
				result[index].Error = err.Error()
			}
		})
	}
	workers.Wait()
	return result, nil
}
