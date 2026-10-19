package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
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

// PUT /v2/gateway changes access and host names at once; a changed listen address is moved to by the
// running gateway, so it needs no restart.
func Test_GatewayUpdateReachSettings(t *testing.T) {
	f := newFixtureConfig(t, gatewayTokenConfig(""))
	var out v2.GatewayUpdateResponse
	body := v2.GatewayUpdateRequest{Listen: ":8080", Access: []string{"localhost", "203.0.113.0/24"}, Hosts: []string{"xmesh.home.example"}}
	decodeAs(t, f.ok("PUT", "/v2/gateway", body, "GatewayUpdateResponse"), &out)
	if out.RestartRequired || out.Listen != ":8080" || len(out.Access) != 2 || out.Hosts[0] != "xmesh.home.example" {
		t.Fatalf("%+v", out)
	}
	if got := f.store.Get().Gateway; strings.Join(got.Access, ",") != "localhost,203.0.113.0/24" || got.Hosts[0] != "xmesh.home.example" {
		t.Fatalf("store has %+v", got)
	}
	body.Listen = "127.0.0.1:8080"
	decodeAs(t, f.ok("PUT", "/v2/gateway", body, "GatewayUpdateResponse"), &out)
	if out.RestartRequired || out.Listen != "127.0.0.1:8080" {
		t.Fatalf("a listen change asked for a restart: %+v", out)
	}
	if got := f.store.Get().Gateway.Listen; got != "127.0.0.1:8080" {
		t.Fatalf("the running generation still has listen %q", got)
	}
	// Malformed access entries, an address used as a host name, and a specific bind address are refused.
	raw, _ := os.ReadFile(f.path)
	for _, bad := range []v2.GatewayUpdateRequest{
		{Listen: ":8080", Access: []string{"everyone"}, Hosts: []string{}},
		{Listen: ":8080", Access: []string{}, Hosts: []string{"192.168.1.5"}},
		{Listen: "192.0.2.5:8080", Access: []string{}, Hosts: []string{}},
	} {
		if r := f.do("PUT", "/v2/gateway", bad, nil); r.Status != 422 {
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
	apiError(t, f.do("PUT", "/v2/gateway", v2.GatewayUpdateRequest{Listen: ":8080", Access: []string{}, Hosts: []string{}}, nil), v2.ErrRoleDisabled)
}

func Test_GatewayDefaultPatRecoversInterruptedCreation(t *testing.T) {
	for _, stage := range []string{"token", "receipt", "both"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixtureConfig(t, gatewayTokenConfig(""))
			dir := f.server.patDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(dir, "default.token")
			if stage != "receipt" {
				if err := os.WriteFile(old, []byte("orphaned-credential"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage != "token" {
				if err := os.WriteFile(old+".owner", []byte("default\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			created, err := EnsureDefaultPAT(f.store, f.server.o.StateDir)
			if err != nil || !created {
				t.Fatalf("recovery: %v %v", created, err)
			}
			pat := f.store.Get().Gateway.PATs[DefaultPATName]
			if pat.TokenFile == old || !managedPatPath(dir, DefaultPATName, pat.TokenFile) {
				t.Fatalf("reused orphan: %+v", pat)
			}
			if stage != "receipt" {
				if body, err := os.ReadFile(old); err != nil || string(body) != "orphaned-credential" {
					t.Fatalf("orphan changed %q %v", body, err)
				}
			}
			if stage != "token" {
				if body, err := os.ReadFile(old + ".owner"); err != nil || string(body) != "default\n" {
					t.Fatalf("receipt changed %q %v", body, err)
				}
			}
			if body, err := os.ReadFile(pat.TokenFile + ".owner"); err != nil || string(body) != "default\n" {
				t.Fatalf("new receipt %q %v", body, err)
			}
			// The recovered managed file follows the normal deletion path.
			if r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
				t.Fatal(r.Status)
			}
			if r := f.do("DELETE", "/v2/pats/default", nil, nil); r.Status != 204 {
				t.Fatal(r.Status)
			}
			if _, err := os.Stat(pat.TokenFile); !os.IsNotExist(err) {
				t.Fatal("recovered token remained", err)
			}
		})
	}
}
