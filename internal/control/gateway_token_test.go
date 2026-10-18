package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
)

// gatewayConfig is a node with the gateway role and a PAT list given by pats (YAML, may be empty).
func gatewayTokenConfig(pats string) string {
	return strings.Replace(testConfig, "roles: [agent]", "roles: [agent, gateway]", 1) + "gateway:\n  listen: \":8080\"\n  access: [localhost, lan, tailnet]\n" + pats
}

// conformance: CTR-CTL-09 the default token is created only when none exists; an upgrade, an update or a
// reinstall keeps the token the node already has, and only a rotation the user asks for replaces it.
func Test_GatewayTokenCreatedOnceAndNeverRotatedByUpgrade(t *testing.T) {
	f := newFixtureConfig(t, gatewayTokenConfig(""))
	created, err := EnsureDefaultPAT(f.store, f.server.o.StateDir)
	if err != nil || !created {
		t.Fatalf("first run: created=%v err=%v", created, err)
	}
	file := filepath.Join(f.server.o.StateDir, "pats", DefaultPATName+".token")
	first, err := os.ReadFile(file)
	if err != nil || len(strings.TrimSpace(string(first))) < 32 {
		t.Fatalf("token file: %v %q", err, first)
	}
	pat, ok := f.store.Get().Gateway.PATs[DefaultPATName]
	if !ok || pat.TokenFile != file || strings.Join(pat.Scopes, ",") != strings.Join(DefaultPATScopes, ",") {
		t.Fatalf("entry %+v", pat)
	}

	// The next start (an upgrade, an update, a reinstall that keeps the state directory) finds the token.
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.NewStore(f.path)
	if err != nil {
		t.Fatal(err)
	}
	created, err = EnsureDefaultPAT(reloaded, f.server.o.StateDir)
	if err != nil || created {
		t.Fatalf("second start rotated or re-created the token: created=%v err=%v", created, err)
	}
	again, _ := os.ReadFile(file)
	if string(again) != string(first) {
		t.Fatal("token file changed on restart")
	}
	if after, _ := os.ReadFile(f.path); string(after) != string(raw) {
		t.Fatal("configuration changed on restart")
	}
}

// An upgraded node with a token from the previous release keeps that token byte for byte, including a
// token file the operator pointed at.
func Test_GatewayUpgradeKeepsExistingToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "operator.token")
	secret := strings.Repeat("e", 43)
	if err := os.WriteFile(tokenFile, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := gatewayTokenConfig("  pats:\n    ops:\n      token_file: " + tokenFile + "\n      scopes: [\"devices:read\"]\n")
	f := newFixtureConfig(t, cfg)
	created, err := EnsureDefaultPAT(f.store, f.server.o.StateDir)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, ok := f.store.Get().Gateway.PATs[DefaultPATName]; ok {
		t.Fatal("a default token was added beside the existing one")
	}
	if got, _ := os.ReadFile(tokenFile); strings.TrimSpace(string(got)) != secret {
		t.Fatal("the operator's token file was rewritten")
	}
}

// A node without the gateway role gets no token.
func Test_GatewayTokenOnlyForGatewayNodes(t *testing.T) {
	f := newFixture(t)
	if created, err := EnsureDefaultPAT(f.store, f.server.o.StateDir); err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, err := os.Stat(filepath.Join(f.server.o.StateDir, "pats", DefaultPATName+".token")); err == nil {
		t.Fatal("a token file was written for a node without a gateway")
	}
}

// PUT /v1/gateway changes access and host names at once; a changed listen address needs a restart.
func Test_GatewayUpdateReachSettings(t *testing.T) {
	f := newFixtureConfig(t, gatewayTokenConfig(""))
	var out v1.GatewayUpdateResponse
	body := v1.GatewayUpdateRequest{Listen: ":8080", Access: []string{"localhost", "203.0.113.0/24"}, Hosts: []string{"xmesh.home.example"}}
	decodeAs(t, f.ok("PUT", "/v1/gateway", body, "GatewayUpdateResponse"), &out)
	if out.RestartRequired || out.Listen != ":8080" || len(out.Access) != 2 || out.Hosts[0] != "xmesh.home.example" {
		t.Fatalf("%+v", out)
	}
	if got := f.store.Get().Gateway; strings.Join(got.Access, ",") != "localhost,203.0.113.0/24" || got.Hosts[0] != "xmesh.home.example" {
		t.Fatalf("store has %+v", got)
	}
	body.Listen = "127.0.0.1:8080"
	decodeAs(t, f.ok("PUT", "/v1/gateway", body, "GatewayUpdateResponse"), &out)
	if !out.RestartRequired {
		t.Fatal("a listen change did not ask for a restart")
	}
	// Malformed access entries, an address used as a host name, and a specific bind address are refused.
	raw, _ := os.ReadFile(f.path)
	for _, bad := range []v1.GatewayUpdateRequest{
		{Listen: ":8080", Access: []string{"everyone"}, Hosts: []string{}},
		{Listen: ":8080", Access: []string{}, Hosts: []string{"192.168.1.5"}},
		{Listen: "192.0.2.5:8080", Access: []string{}, Hosts: []string{}},
	} {
		if r := f.do("PUT", "/v1/gateway", bad, nil); r.Status != 422 {
			t.Fatalf("%+v accepted: %d %s", bad, r.Status, r.Body)
		}
	}
	if after, _ := os.ReadFile(f.path); string(after) != string(raw) {
		t.Fatal("a refused update changed the file")
	}
}

// A node without the gateway role has no gateway to reach.
func Test_GatewayUpdateNeedsGatewayRole(t *testing.T) {
	f := newFixture(t)
	apiError(t, f.do("PUT", "/v1/gateway", v1.GatewayUpdateRequest{Listen: ":8080", Access: []string{}, Hosts: []string{}}, nil), v1.ErrRoleDisabled)
}
