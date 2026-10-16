package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func authorityRead(t *testing.T, s *mcp.ClientSession, request pty.AuthorityRequest) (pty.AuthorityReply, error) {
	t.Helper()
	raw, _ := json.Marshal(request)
	uri := pty.BudgetAuthorityURI + "?q=" + base64.RawURLEncoding.EncodeToString(raw)
	out, err := s.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return pty.AuthorityReply{}, err
	}
	var reply pty.AuthorityReply
	if len(out.Contents) != 1 || json.Unmarshal([]byte(out.Contents[0].Text), &reply) != nil {
		t.Fatal("bad authority reply", out)
	}
	return reply, nil
}

func TestPTYBudgetAuthorityAuthorizationAndNodeBinding(t *testing.T) {
	_, source := configuration(t)
	c := *source.Get()
	c.Agent.Authz = map[string]config.Grant{"tag:ingress": {Tools: []string{"pty.*"}}, "tag:peer": {Tools: []string{"*"}}}
	a, err := New(config.Static(&c))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.BudgetAuthority = pty.NewBudgetAuthorityTTL(time.Second)
	time.Sleep(1100 * time.Millisecond)
	session := func(p Principal) *mcp.ClientSession {
		server := httptest.NewServer(a.HTTP(func(*http.Request) (Principal, error) { return p, nil }))
		t.Cleanup(server.Close)
		return connect(t, server.URL+"/mcp")
	}
	for _, p := range []Principal{
		{Node: "peer", Tags: []string{"tag:peer"}},
		{Node: "ingress", Tags: []string{"tag:ingress"}},
		{Node: "denied", Tags: []string{"tag:ingress-denied"}, OAuthClient: "client"},
	} {
		if _, err := authorityRead(t, session(p), pty.AuthorityRequest{Op: "acquire", Owner: "n"}); err == nil {
			t.Fatalf("%+v reached the budget authority", p)
		}
	}
	one := session(Principal{Node: "ingress-one", Tags: []string{"tag:ingress"}, OAuthClient: "client"})
	two := session(Principal{Node: "ingress-two", Tags: []string{"tag:ingress"}, OAuthClient: "client"})
	held, err := authorityRead(t, one, pty.AuthorityRequest{Op: "acquire", Owner: "same-nonce"})
	if err != nil || !held.OK {
		t.Fatal(held, err)
	}
	// The same nonce from another authenticated node is a different owner.
	if other, err := authorityRead(t, two, pty.AuthorityRequest{Op: "acquire", Owner: "same-nonce"}); err != nil || other.OK || !other.Busy {
		t.Fatal("second node shared the lease", other, err)
	}
	if forged, err := authorityRead(t, two, pty.AuthorityRequest{Op: "release", Owner: "same-nonce", Token: held.Token, Outcome: "ok"}); err != nil || forged.OK {
		t.Fatal("second node released the first node's lease", forged, err)
	}
	if done, err := authorityRead(t, one, pty.AuthorityRequest{Op: "release", Owner: "same-nonce", Token: held.Token, Outcome: "ok"}); err != nil || !done.OK {
		t.Fatal(done, err)
	}
	for _, uri := range []string{pty.BudgetAuthorityURI, pty.BudgetAuthorityURI + "?q=!!", pty.BudgetAuthorityURI + "?q=" + base64.RawURLEncoding.EncodeToString([]byte(`{"op":"acquire"}`))} {
		if _, err := one.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Fatal("malformed authority request accepted", uri)
		}
	}
	// Policy reloads revoke the authority exactly as they revoke the snapshot.
	delete(c.Agent.Authz, "tag:ingress")
	if _, err := authorityRead(t, one, pty.AuthorityRequest{Op: "acquire", Owner: "same-nonce"}); err == nil {
		t.Fatal("revoked authorization retained authority access")
	}
}
