package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// ingressBudgetCaller re-authorizes the session on every control read so a
// policy reload revokes budget access immediately.
func (a *Agent) ingressBudgetCaller(req *mcp.ReadResourceRequest, principal Principal) error {
	caller := a.bind(req.Session, principal)
	current, exists := a.Callers.Get(caller.ID)
	cfg := a.store.Get()
	if !exists || !slices.Contains(current.Tags, "tag:ingress") || current.OAuthClient == "" || (!Allowed(cfg, current, "pty.open", "") && !Allowed(cfg, current, "pty.transfer", "")) {
		return toolerr.New("unauthorized", "PTY 预算只允许已授权的入口查询")
	}
	return nil
}

func (a *Agent) installPTYBudget(server *mcp.Server, principal Principal) {
	if !slices.Contains(principal.Tags, "tag:ingress") {
		return
	}
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: pty.BudgetAuthorityURI + "{?q}", Name: "pty-budget-authority", MIMEType: "application/json"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if err := a.ingressBudgetCaller(req, principal); err != nil {
			return nil, err
		}
		u, err := url.Parse(req.Params.URI)
		if err != nil || u.Scheme+"://"+u.Host+u.Path != pty.BudgetAuthorityURI || len(u.RawQuery) > 4096 {
			return nil, toolerr.Args("uri")
		}
		raw, err := base64.RawURLEncoding.DecodeString(u.Query().Get("q"))
		var request pty.AuthorityRequest
		if err != nil || json.Unmarshal(raw, &request) != nil || request.Owner == "" || len(request.Owner) > 128 {
			return nil, toolerr.Args("q")
		}
		// The node identity comes from the authenticated transport, not the request.
		reply, err := a.BudgetAuthority.Handle(request, principal.Node+"/"+request.Owner)
		if err != nil {
			return nil, toolerr.Args("q")
		}
		body, err := json.Marshal(reply)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(body)}}}, nil
	})
	server.AddResource(&mcp.Resource{URI: pty.BudgetURI, Name: "pty-budget", MIMEType: "application/json"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if err := a.ingressBudgetCaller(req, principal); err != nil {
			return nil, err
		}
		body, err := json.Marshal(a.PTY.BudgetSnapshot())
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: pty.BudgetURI, MIMEType: "application/json", Text: string(body)}}}, nil
	})
}
