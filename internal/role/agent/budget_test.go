package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/pty"
)

func TestPTYBudgetResourceAuthorization(t *testing.T) {
	_, source := configuration(t)
	c := *source.Get()
	c.Agent.Authz = map[string]config.Grant{"tag:ingress": {Tools: []string{"pty.*"}}, "tag:peer": {Tools: []string{"*"}}}
	a, err := New(config.Static(&c))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	for _, principal := range []Principal{
		{Node: "peer", Tags: []string{"tag:peer"}},
		{Node: "ingress", Tags: []string{"tag:ingress"}, OAuthClient: "client"},
		{Node: "ingress", Tags: []string{"tag:ingress"}},
		{Node: "denied", Tags: []string{"tag:ingress-denied"}, OAuthClient: "client"},
	} {
		t.Run(principal.Node+principal.OAuthClient, func(t *testing.T) {
			server := httptest.NewServer(a.HTTP(func(*http.Request) (Principal, error) { return principal, nil }))
			defer server.Close()
			s := connect(t, server.URL+"/mcp")
			defer s.Close()
			read := func() (*mcp.ReadResourceResult, error) {
				return s.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: pty.BudgetURI})
			}
			out, err := read()
			allowed := principal.Node == "ingress" && principal.OAuthClient == "client"
			if !allowed {
				if err == nil {
					t.Fatal("untrusted resource read accepted")
				}
				return
			}
			if err != nil || len(out.Contents) != 1 {
				t.Fatal(out, err)
			}
			var snapshot pty.BudgetSnapshot
			if err := json.Unmarshal([]byte(out.Contents[0].Text), &snapshot); err != nil || snapshot.Validate() != nil {
				t.Fatal("invalid snapshot", err)
			}
			// Configuration generations may revoke control-resource authorization.
			delete(c.Agent.Authz, "tag:ingress")
			if _, err := read(); err == nil {
				t.Fatal("revoked authorization retained budget access")
			}
		})
	}
}
