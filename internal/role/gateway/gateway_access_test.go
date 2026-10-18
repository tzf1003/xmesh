package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// conformance: CTR-GW-07 every request needs a token; the source must be in gateway.access; the Host
// header must name an address or a configured host.
func Test_GatewayRequiresTokenFromAnySource(t *testing.T) {
	token := strings.Repeat("c", 43)
	file := filepath.Join(t.TempDir(), "default.token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pats := map[string]config.PAT{"default": {TokenFile: file, Scopes: []string{"devices:read"}}}
	request := func(c *config.Config, remote, host, bearer string) error {
		t.Helper()
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = remote
		r.Host = host
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		_, err := LocalAuthentication(config.Static(c))(r)
		return err
	}
	all := &config.Config{Gateway: config.Gateway{Access: []string{"localhost", "lan", "tailnet"}, PATs: pats}}

	if err := request(all, "127.0.0.1:2345", "127.0.0.1:8080", ""); err == nil {
		t.Fatal("loopback served without a token")
	}
	if err := request(all, "127.0.0.1:2345", "127.0.0.1:8080", token); err != nil {
		t.Fatal("loopback with the token refused:", err)
	}
	if err := request(all, "192.168.1.20:2345", "192.168.1.5:8080", token); err != nil {
		t.Fatal("LAN client with the token refused:", err)
	}
	if err := request(all, "192.168.1.20:2345", "192.168.1.5:8080", ""); err == nil {
		t.Fatal("LAN client served without a token")
	}
	if err := request(all, "100.100.1.2:2345", "100.100.1.2:8080", token); err != nil {
		t.Fatal("tailnet client with the token refused:", err)
	}

	localOnly := &config.Config{Gateway: config.Gateway{Access: []string{"localhost"}, PATs: pats}}
	if err := request(localOnly, "192.168.1.20:2345", "192.168.1.5:8080", token); err == nil {
		t.Fatal("LAN client served though access is localhost only")
	}
	if err := request(all, "8.8.8.8:2345", "8.8.8.8:8080", token); err == nil {
		t.Fatal("public source served")
	}

	withHost := &config.Config{Gateway: config.Gateway{Access: []string{"lan"}, Hosts: []string{"xmesh.home.example"}, PATs: pats}}
	if err := request(withHost, "192.168.1.20:2345", "xmesh.home.example:8080", token); err != nil {
		t.Fatal("configured host refused:", err)
	}
	if err := request(withHost, "192.168.1.20:2345", "evil.example:8080", token); err == nil {
		t.Fatal("unconfigured host name served (DNS rebinding)")
	}
}

// With no PAT there is nothing to check a token against, so nothing is served, loopback included.
func Test_GatewayWithoutPATServesNothing(t *testing.T) {
	c := &config.Config{Gateway: config.Gateway{Access: []string{"localhost", "lan", "tailnet"}}}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.RemoteAddr = "127.0.0.1:2345"
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("d", 43))
	if _, err := LocalAuthentication(config.Static(c))(r); err == nil {
		t.Fatal("served with no PAT configured")
	}
}
