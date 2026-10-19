package app

import (
	"context"
	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/connectivity"
)

type runtimeApplier struct {
	roles   *roleRuntime
	network *connectivity.Node
}

func (a runtimeApplier) Settle(ctx context.Context, retry bool) []v2.ApplyError {
	out := a.roles.Settle(ctx, retry)
	if a.network != nil {
		out = append(out, a.network.Settle(ctx)...)
	}
	return out
}
