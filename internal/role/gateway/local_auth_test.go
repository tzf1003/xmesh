package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
)

func Test_PATRotationAndFileProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pat")
	first, second := strings.Repeat("a", 43), strings.Repeat("b", 43)
	write := func(secret string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(first)
	c := &config.Config{Gateway: config.Gateway{PATs: map[string]config.PAT{"reader": {TokenFile: path, Scopes: []string{"fs:read:device"}}}}}
	auth := LocalAuthentication(config.Static(c))
	request := func(token, remote string) (Principal, error) {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = remote
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return auth(r)
	}
	for _, token := range []string{"", second} {
		if _, err := request(token, "127.0.0.1:2345"); err == nil {
			t.Fatal("invalid bearer accepted")
		}
	}
	p, err := request(first, "127.0.0.1:2345")
	if err != nil || p.Local || p.ClientID != "" || p.CredentialID != "pat:reader" {
		t.Fatal(p, err)
	}
	if _, err := request(first, "192.0.2.1:2345"); err == nil {
		t.Fatal("non-loopback accepted")
	}
	write(second + "\n")
	if _, err := request(first, "127.0.0.1:2345"); err == nil {
		t.Fatal("rotated token accepted")
	}
	if _, err := request(second, "127.0.0.1:2345"); err != nil {
		t.Fatal(err)
	}
	c.Gateway.PATs["duplicate"] = c.Gateway.PATs["reader"]
	if _, err := request(second, "127.0.0.1:2345"); err == nil {
		t.Fatal("ambiguous identity accepted")
	}
	delete(c.Gateway.PATs, "duplicate")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := request(second, "127.0.0.1:2345"); err == nil {
			t.Fatal("public credential file accepted")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("short")
	if _, err := request(second, "127.0.0.1:2345"); err == nil {
		t.Fatal("invalid stored credential accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := request(second, "127.0.0.1:2345"); err == nil {
		t.Fatal("missing file accepted")
	}
	c.Gateway.PATs = nil
	if p, err := request("", "127.0.0.1:2345"); err != nil || !p.Local {
		t.Fatal(p, err)
	}
	if _, err := request(second, "127.0.0.1:2345"); err == nil {
		t.Fatal("unknown bearer downgraded to local")
	}
}

func Test_PATGatewayScopesAndSessionBinding(t *testing.T) {
	g, root := fixture(t, false)
	path := filepath.Join(t.TempDir(), "pat")
	token := strings.Repeat("r", 43)
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readable"), []byte("PAT roundtrip"), 0600); err != nil {
		t.Fatal(err)
	}
	pat := config.PAT{TokenFile: path, Scopes: []string{"devices:read", "fs:read:test-node"}}
	c := &config.Config{Gateway: config.Gateway{PATs: map[string]config.PAT{"reader": pat}}}
	otherPath := filepath.Join(t.TempDir(), "other-pat")
	other := strings.Repeat("s", 43)
	if err := os.WriteFile(otherPath, []byte(other), 0600); err != nil {
		t.Fatal(err)
	}
	otherPAT := pat
	otherPAT.TokenFile = otherPath
	c.Gateway.PATs["other"] = otherPAT
	server := httptest.NewServer(g.HTTP(LocalAuthentication(config.Static(c))))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "pat-test", Version: "1"}, nil)
	s, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	list, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "local/fs.read" {
			found = true
		}
		if strings.HasSuffix(tool.Name, "fs.write") || strings.Contains(tool.Name, "pty.") {
			t.Fatal("scope catalog", tool.Name)
		}
	}
	if !found {
		t.Fatal("scoped file tools missing")
	}
	if out := call(t, s, "local/fs.read", map[string]any{"path": "readable"}); out.IsError || !strings.Contains(object(t, out)["content"].(string), "PAT roundtrip") {
		t.Fatal(out)
	}
	if out := call(t, s, "local/fs.write", map[string]any{"path": "blocked", "content": "blocked"}); !out.IsError || object(t, out)["error"] != "unauthorized" {
		t.Fatal(out)
	}
	// Another PAT with identical scope still has a distinct MCP session identity.
	r, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":99,"method":"tools/list","params":{}}`))
	r.Header.Set("Authorization", "Bearer "+other)
	r.Header.Set("Mcp-Session-Id", s.ID())
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("credential session binding", resp.StatusCode)
	}
}
