package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func Test_Gate4_OAuthGatewayAgent(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	doc, _ := json.Marshal(map[string]any{"node": map[string]any{"name": "device", "roles": []string{"agent"}}, "mesh": map[string]any{"mode": "lan", "lan": map[string]any{"mdns": true}}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:ingress": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}})
	c, e := config.Parse(doc)
	if e != nil {
		t.Fatal(e)
	}
	a, e := agent.New(config.Static(c))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	agentHTTP := httptest.NewServer(a.HTTP(func(r *http.Request) (agent.Principal, error) {
		return identity.FromPeer("stable-ingress", []string{"tag:ingress"}, r.Header.Get("X-Xmesh-Oauth-Client"))
	}))
	t.Cleanup(agentHTTP.Close)
	g := New(Options{NodeName: "device", Ingress: true, LocalPeer: mesh.Peer{Name: "device", ID: "device", Address: agentHTTP.URL + "/mcp"}, Dial: RemoteDial(agentHTTP.Client())})
	t.Cleanup(g.Close)
	authorization, e := oauth.New(oauth.Options{Issuer: "https://mesh.example", Password: "integration-login-fixture", StateDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(authorization.Handler(g.HTTP(func(r *http.Request) (Principal, error) {
		token, ok := oauth.FromContext(r.Context())
		if !ok {
			t.Fatal("missing authenticated context")
		}
		return Principal{ClientID: token.ClientID, Scopes: token.Scopes}, nil
	})))
	t.Cleanup(server.Close)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, body, content, cookie string) (int, http.Header, string) {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", content)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		resp, e := noRedirect.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	login := func() (string, map[string]string) {
		t.Helper()
		status, _, body := request("POST", "/oauth/register", `{"client_name":"integration","redirect_uris":["http://127.0.0.1:1234/callback"],"token_endpoint_auth_method":"none"}`, "application/json", "")
		if status != 201 {
			t.Fatal(status, body)
		}
		var client map[string]any
		_ = json.Unmarshal([]byte(body), &client)
		id := client["client_id"].(string)
		verifier := strings.Repeat("v", 43)
		hash := sha256.Sum256([]byte(verifier))
		q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"fixture-state"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "scope": {"devices:read sys:* fs:* pty:*"}}
		path := "/oauth/authorize?" + q.Encode()
		status, headers, body := request("GET", path, "", "", "")
		if status != 200 {
			t.Fatal(status, body)
		}
		csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
		if len(csrf) != 2 {
			t.Fatal("CSRF form missing")
		}
		status, headers, body = request("POST", path, url.Values{"password": {"integration-login-fixture"}, "csrf": {csrf[1]}}.Encode(), "application/x-www-form-urlencoded", strings.Split(headers.Get("Set-Cookie"), ";")[0])
		if status != 302 {
			t.Fatal(status, body)
		}
		redirect, _ := url.Parse(headers.Get("Location"))
		form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code": {redirect.Query().Get("code")}, "code_verifier": {verifier}}
		status, _, body = request("POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded", "")
		if status != 200 {
			t.Fatal(status, body)
		}
		var tokens map[string]string
		var values map[string]any
		_ = json.Unmarshal([]byte(body), &values)
		tokens = map[string]string{"access_token": values["access_token"].(string), "refresh_token": values["refresh_token"].(string)}
		return id, tokens
	}
	connectBearer := func(token string) *mcp.ClientSession {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client := mcp.NewClient(&mcp.Implementation{Name: "oauth-test", Version: "1"}, nil)
		s, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	id, tokens := login()
	one := connectBearer(tokens["access_token"])
	two := connectBearer(tokens["access_token"])
	if o := call(t, one, "device/fs.write", map[string]any{"path": "roundtrip", "content": "OAuth to Agent"}); o.IsError {
		t.Fatal(o)
	}
	if o := call(t, two, "device/fs.read", map[string]any{"path": "roundtrip"}); o.IsError || !strings.Contains(object(t, o)["content"].(string), "OAuth to Agent") {
		t.Fatal(o)
	}
	first := object(t, call(t, one, "device/pty.open", map[string]any{"cols": 80, "rows": 24}))
	second := object(t, call(t, two, "device/pty.open", map[string]any{"cols": 80, "rows": 24}))
	if first["owner"] == second["owner"] || !strings.HasPrefix(first["owner"].(string), "stable-ingress/") {
		t.Fatal(first, second)
	}
	if e := one.Close(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	orphaned := false
	for time.Now().Before(deadline) {
		rows := object(t, call(t, two, "device/pty.list", map[string]any{}))["sessions"].([]any)
		for _, value := range rows {
			row := value.(map[string]any)
			if row["id"] == first["session_id"] && row["state"] == "orphaned" {
				orphaned = true
			}
		}
		if orphaned {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !orphaned {
		t.Fatal("downstream disconnect did not orphan terminal")
	}
	_, otherTokens := login()
	other := connectBearer(otherTokens["access_token"])
	third := object(t, call(t, other, "device/pty.open", map[string]any{"cols": 80, "rows": 24}))
	transfer := call(t, other, "device/pty.transfer", map[string]any{"session_id": first["session_id"], "to_caller": third["owner"]})
	if !transfer.IsError || object(t, transfer)["error"] != "not_owner" {
		t.Fatal("OAuth client boundary", transfer)
	}
	if o := call(t, two, "device/pty.transfer", map[string]any{"session_id": first["session_id"], "to_caller": second["owner"]}); o.IsError {
		t.Fatal(o)
	}
	for _, terminal := range []any{first["session_id"], second["session_id"]} {
		if o := call(t, two, "device/pty.close", map[string]any{"session_id": terminal}); o.IsError {
			t.Fatal(o)
		}
	}
	if o := call(t, other, "device/pty.close", map[string]any{"session_id": third["session_id"]}); o.IsError {
		t.Fatal(o)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tokens["refresh_token"]}, "scope": {"devices:read fs:read:*"}}
	status, _, body := request("POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded", "")
	if status != 200 {
		t.Fatal(status, body)
	}
	var narrower map[string]any
	_ = json.Unmarshal([]byte(body), &narrower)
	r, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":100,"method":"tools/list","params":{}}`))
	r.Header.Set("Authorization", "Bearer "+narrower["access_token"].(string))
	r.Header.Set("Mcp-Session-Id", two.ID())
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("narrowed token reused broader session", resp.StatusCode)
	}
	narrow := connectBearer(narrower["access_token"].(string))
	list, e := narrow.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range list.Tools {
		if strings.Contains(tool.Name, "pty.") || strings.HasSuffix(tool.Name, "fs.write") {
			t.Fatal("narrowed catalog", tool.Name)
		}
	}
}
