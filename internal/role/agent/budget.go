package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func (a *Agent) installPTYBudget(server *mcp.Server, principal Principal) {
	if !slices.Contains(principal.Tags, "tag:ingress") {
		return
	}
	server.AddResource(&mcp.Resource{URI: pty.BudgetURI, Name: "pty-budget", MIMEType: "application/json"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		caller := a.bind(req.Session, principal)
		current, exists := a.Callers.Get(caller.ID)
		cfg := a.store.Get()
		if !exists || !slices.Contains(current.Tags, "tag:ingress") || current.OAuthClient == "" || (!Allowed(cfg, current, "pty.open", "") && !Allowed(cfg, current, "pty.transfer", "")) {
			return nil, toolerr.New("unauthorized", "PTY 预算只允许已授权的入口查询")
		}
		body, err := json.Marshal(a.PTY.BudgetSnapshot())
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: pty.BudgetURI, MIMEType: "application/json", Text: string(body)}}}, nil
	})
}
