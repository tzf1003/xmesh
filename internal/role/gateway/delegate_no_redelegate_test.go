package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/delegateguard"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

const gwProbeMarker = "--xmesh-gw-redelegate-probe"

func TestMain(m *testing.M) {
	delegateguard.HandleReaper(os.Args)
	if len(os.Args) >= 6 && os.Args[1] == gwProbeMarker {
		os.Exit(runGatewayProbe(os.Args[2], os.Args[3], os.Args[4], os.Args[5] == "spawn"))
	}
	os.Exit(m.Run())
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func runGatewayProbe(endpoint, token, peerName string, spawnChild bool) int {
	_ = os.Unsetenv("XMESH_DELEGATE")
	if spawnChild {
		exe, err := os.Executable()
		if err != nil {
			return 2
		}
		if out, err := exec.Command(exe, gwProbeMarker, endpoint, token, peerName, "direct").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "child probe failed: %v (%s)\n", err, out)
			return 3
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "gw-probe", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 4
	}
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 5
	}
	for _, tl := range list.Tools {
		if strings.Contains(tl.Name, "delegate.") {
			fmt.Fprintf(os.Stderr, "unexpected delegate tool visible: %s\n", tl.Name)
			return 6
		}
	}

	// tools.search must hide delegate.* from delegated callers.
	searchRes, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "tools.search", Arguments: map[string]any{"pattern": "delegate"}})
	if err != nil || searchRes.IsError {
		fmt.Fprintf(os.Stderr, "tools.search failed: %v %v\n", err, searchRes)
		return 7
	}
	if len(searchRes.Content) > 0 {
		if tc, ok := searchRes.Content[0].(*mcp.TextContent); ok {
			var body map[string]any
			_ = json.Unmarshal([]byte(tc.Text), &body)
			if tools, ok := body["tools"].([]any); ok && len(tools) != 0 {
				fmt.Fprintf(os.Stderr, "tools.search returned delegate tools: %v\n", tools)
				return 8
			}
		}
	}

	// Direct device-prefixed, local/, bare (with default device), and tools.call attempts must all be denied with delegate.redelegate_denied.
	attempts := []struct {
		name string
		args map[string]any
	}{
		{peerName + "/delegate.list", map[string]any{}},
		{peerName + "/delegate.run", map[string]any{"runner": "claude", "prompt": "hi"}},
		{"local/delegate.list", map[string]any{}},
		{"local/delegate.run", map[string]any{"runner": "claude", "prompt": "hi"}},
		{"tools.call", map[string]any{"device": peerName, "tool": "delegate.list", "args": map[string]any{}}},
		{"tools.call", map[string]any{"device": "local", "tool": "delegate.run", "args": map[string]any{"runner": "claude", "prompt": "hi"}}},
	}
	for _, a := range attempts {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: a.name, Arguments: a.args})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s call error: %v\n", a.name, err)
			return 9
		}
		if !res.IsError {
			fmt.Fprintf(os.Stderr, "%s unexpectedly succeeded\n", a.name)
			return 10
		}
		var body map[string]any
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				_ = json.Unmarshal([]byte(tc.Text), &body)
			}
		}
		if body["error"] != "policy_denied" || body["rule"] != "delegate.redelegate_denied" {
			fmt.Fprintf(os.Stderr, "%s got error=%v rule=%v\n", a.name, body["error"], body["rule"])
			return 11
		}
	}
	return 0
}

// conformance: CTR-DLG-06
func Test_DelegatedSubprocessCannotRedelegateThroughGatewayWithPAT(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const patSecret = "xmesh-pat-secret-0123456789abcdef012345"
	tokenFile := filepath.Join(root, "default.token")
	if err := os.WriteFile(tokenFile, []byte(patSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"node": map[string]any{"name": "gw-node", "roles": []string{"agent", "gateway"}},
		"mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}},
		"gateway": map[string]any{
			"listen": "127.0.0.1:17434",
			"pats": map[string]any{
				"default": map[string]any{"token_file": tokenFile, "scopes": []string{"exec:*", "devices:read"}},
			},
		},
		"agent": map[string]any{
			"default_cwd": root,
			"cwd_policy":  map[string]any{"allow": []string{root}},
			"authz":       map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}},
			"pty":         map[string]any{"default_shell": "auto"},
			"delegate":    map[string]any{"enabled": true},
		},
	}
	b, _ := json.Marshal(doc)
	cfgPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	a, err := agent.New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	agentSrv := httptest.NewUnstartedServer(a.HTTP(agent.Local))
	agentSrv.Config.ConnContext = a.ConnContext()
	agentSrv.Start()
	t.Cleanup(agentSrv.Close)

	guard := delegateguard.New()
	g := New(Options{
		NodeName:      "gw-node",
		LocalPeer:     mesh.Peer{ID: "local", Name: "gw-node", Address: agentSrv.URL + "/mcp", Online: true, Tags: []string{"tag:device"}},
		Dial:          RemoteDial(agentSrv.Client()),
		DelegateGuard: guard,
	})
	t.Cleanup(g.Close)

	gwSrv := httptest.NewUnstartedServer(g.HTTP(LocalAuthentication(store)))
	gwSrv.Config.ConnContext = g.ConnContext()
	gwSrv.Start()
	t.Cleanup(gwSrv.Close)

	gwEndpoint := gwSrv.URL + "/mcp"

	// 1. Normal caller with the same exec:* PAT can list and call delegate.* tools.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "normal", Version: "1"}, nil)
	normalSession, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   gwEndpoint,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: patSecret}},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = normalSession.Close() })
	res := call(t, normalSession, "local/delegate.list", map[string]any{})
	if res.IsError {
		t.Fatalf("normal PAT caller could not call local/delegate.list: %v", object(t, res))
	}

	// 2. Delegated subprocess (with child + unset XMESH_DELEGATE) presenting the EXACT SAME exec:* PAT is denied.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, gwProbeMarker, gwEndpoint, patSecret, "gw-node", "spawn")
	cmd.Env = append(os.Environ(), "XMESH_DELEGATE=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	doneStart := guard.BeginStart()
	if err := cmd.Start(); err != nil {
		doneStart()
		t.Fatal(err)
	}
	untrack := guard.Track(cmd.Process.Pid, 0)
	doneStart()
	outErr := cmd.Wait()
	untrack()
	if outErr != nil {
		t.Fatalf("delegated gateway PAT probe failed: %v (%s)", outErr, stderr.String())
	}

	// 3. Normal caller remains unaffected.
	res2 := call(t, normalSession, "local/delegate.list", map[string]any{})
	if res2.IsError {
		t.Fatalf("normal PAT caller affected after probe: %v", object(t, res2))
	}
	_ = cwd.Policy{}
}
