package app

import (
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/role/gateway"
)

// SurfaceFromConfig builds the gateway tool surface from gateway.tool_surface and its companions.
func SurfaceFromConfig(c config.Gateway) gateway.Surface {
	return gateway.Surface{
		Mode:         c.ToolSurface,
		ListedGroups: append([]string(nil), c.ListedGroups...),
		PinnedTools:  append([]string(nil), c.PinnedTools...),
		MaxListed:    c.MaxListedTools,
	}
}
