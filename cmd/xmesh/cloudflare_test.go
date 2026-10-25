package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/cfadmin"
	"github.com/tzf1003/xmesh/internal/cfadmin/cftest"
)

const (
	cfAccount = "0123456789abcdef0123456789abcdef"
	cfZone    = "fedcba9876543210fedcba9876543210"
	cfToken   = "cf-API-TOKEN-VALUE"
)

func cloudflareFixture(t *testing.T) (*cftest.Fake, string, []string) {
	t.Helper()
	fake, url, done := cftest.New()
	t.Cleanup(done)
	old, oldAPI := cloudflareStore, cloudflareAPI
	cloudflareStore, cloudflareAPI = &cfadmin.MemoryStore{}, url
	t.Cleanup(func() { cloudflareStore, cloudflareAPI = old, oldAPI })
	dir := t.TempDir()
	cfg := filepath.Join(dir, "xmesh.yaml")
	body := "# my node\nschema_version: 2\nnode:\n  name: laptop\n  roles: [agent]\nmesh:\n  lan:\n    enabled: false\nagent:\n  cwd_policy:\n    allow: []\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return fake, cfg, []string{"--state-file", filepath.Join(dir, "cf-state.json")}
}

func cloudflareLogin_(t *testing.T) {
	t.Helper()
	var out bytes.Buffer
	args := []string{"login", "--account", cfAccount, "--zone", cfZone, "--domain", "mesh.example.com"}
	if err := cloudflareRun(args, &out, strings.NewReader(cfToken+"\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), cfToken) {
		t.Fatal("login printed the token")
	}
}

func TestCloudflareSetupCreatesResourcesAndWritesConfig(t *testing.T) {
	fake, cfg, state := cloudflareFixture(t)
	cloudflareLogin_(t)
	var out bytes.Buffer
	run := func(args ...string) error { out.Reset(); return cloudflareRun(append(args, state...), &out, nil) }

	if err := run("setup", "--name", "laptop", "--config", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Things) != 0 {
		t.Fatalf("preview created %v", fake.Things)
	}
	if err := run("setup", "--name", "laptop", "--config", cfg, "--apply"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "tunnel-token-") {
		t.Fatal("setup printed the tunnel token")
	}
	raw, _ := os.ReadFile(cfg)
	text := string(raw)
	for _, want := range []string{"# my node", "enabled: true", "hostname: xmesh-laptop-", ".mesh.example.com", "access_client_id: cid-", "tunnel_token_file: " + filepath.Join(filepath.Dir(cfg), "cloudflare", "tunnel-token")} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	token, err := os.ReadFile(filepath.Join(filepath.Dir(cfg), "cloudflare", "tunnel-token"))
	if err != nil || !strings.HasPrefix(string(token), "tunnel-token-") {
		t.Fatalf("%q %v", token, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Join(filepath.Dir(cfg), "cloudflare", "access-secret")); info.Mode().Perm() != 0o600 {
			t.Fatalf("secret mode %v", info.Mode())
		}
	}
	if fake.Service != "tcp://127.0.0.1:7434" {
		t.Fatalf("routed to %q", fake.Service)
	}
	if err := run("status"); err != nil || !strings.Contains(out.String(), "laptop") {
		t.Fatalf("%v %s", err, out.String())
	}
	if err := run("remove", "--name", "laptop", "--apply"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Things) != 0 {
		t.Fatalf("left behind %v", fake.Things)
	}
}

func TestCloudflareLoginRejectsBadInputAndKeepsTokenOutOfErrors(t *testing.T) {
	cloudflareFixture(t)
	var out bytes.Buffer
	for _, args := range [][]string{
		{"login", "--account", "short", "--zone", cfZone, "--domain", "mesh.example.com"},
		{"login", "--account", cfAccount, "--zone", cfZone, "--domain", "NotADomain"},
	} {
		err := cloudflareRun(args, &out, strings.NewReader(cfToken))
		if err == nil || strings.Contains(err.Error(), cfToken) {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := cloudflareRun([]string{"setup", "--name", "x", "--config", "x.yaml"}, &out, nil); err == nil {
		t.Fatal("setup without a login succeeded")
	}
}

func TestCloudflareSetupRoutesToTheConfiguredListener(t *testing.T) {
	fake, cfg, state := cloudflareFixture(t)
	cloudflareLogin_(t)
	body := "schema_version: 2\nnode:\n  name: laptop\n  roles: [agent]\nmesh:\n  lan:\n    enabled: false\n  cloudflare:\n    enabled: true\n    listen: 127.0.0.1:9000\nagent:\n  cwd_policy:\n    allow: []\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cloudflareRun(append([]string{"setup", "--name", "laptop", "--config", cfg, "--apply"}, state...), &out, nil); err != nil {
		t.Fatal(err)
	}
	if fake.Service != "tcp://127.0.0.1:9000" {
		t.Fatalf("routed to %q", fake.Service)
	}
	raw, _ := os.ReadFile(cfg)
	if !strings.Contains(string(raw), "listen: 127.0.0.1:9000") {
		t.Fatalf("listen was lost:\n%s", raw)
	}
}

func TestCloudflareSetupRejectsUnloadableConfigBeforeCreatingAnything(t *testing.T) {
	fake, cfg, state := cloudflareFixture(t)
	cloudflareLogin_(t)
	body := "schema_version: 2\nnode:\n  name: laptop\n  roles: [agent]\nmesh:\n  lan:\n    enabled: false\n  cloudflare:\n    enabled: false\n    listen: not-an-address\nagent:\n  cwd_policy:\n    allow: []\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cloudflareRun(append([]string{"setup", "--name", "laptop", "--config", cfg, "--apply"}, state...), &out, nil); err == nil {
		t.Fatal("setup accepted a config that cannot load")
	}
	if len(fake.Things) != 0 {
		t.Fatalf("created %v before failing", fake.Things)
	}
}

func TestCloudflareRejectsProfileThatEscapesTheStateDirectory(t *testing.T) {
	cloudflareFixture(t)
	var out bytes.Buffer
	if err := cloudflareRun([]string{"status", "--profile", "../../x"}, &out, nil); err == nil {
		t.Fatal("profile with a path was accepted")
	}
}

func TestCloudflareLoginFindsAccountAndZoneFromTheDomain(t *testing.T) {
	fake, _, _ := cloudflareFixture(t)
	fake.AccountList = []map[string]any{{"id": cfAccount, "name": "Me"}}
	fake.ZoneList = []map[string]any{
		{"id": "11111111111111111111111111111111", "name": "other.org", "account": map[string]any{"id": cfAccount}},
		{"id": cfZone, "name": "example.com", "account": map[string]any{"id": cfAccount}},
	}
	var out bytes.Buffer
	if err := cloudflareRun([]string{"login", "--json", "--domain", "mesh.example.com"}, &out, strings.NewReader(cfToken+"\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), cfToken) {
		t.Fatal("login printed the token")
	}
	cred, err := cloudflareStore.Get("default")
	if err != nil || cred.AccountID != cfAccount || cred.ZoneID != cfZone || cred.Domain != "mesh.example.com" {
		t.Fatalf("saved %v, %v", cred, err)
	}
}

func TestCloudflareLoginFindsZoneEvenWhenAccountsCannotBeListed(t *testing.T) {
	fake, _, _ := cloudflareFixture(t)
	fake.ZoneList = []map[string]any{{"id": cfZone, "name": "example.com", "account": map[string]any{"id": cfAccount}}}
	if err := cloudflareRun([]string{"login", "--domain", "mesh.example.com"}, &bytes.Buffer{}, strings.NewReader(cfToken+"\n")); err != nil {
		t.Fatal(err)
	}
	cred, _ := cloudflareStore.Get("default")
	if cred.AccountID != cfAccount || cred.ZoneID != cfZone {
		t.Fatalf("saved %v", cred)
	}
}

func TestCloudflareLoginExplainsWhyItCannotFindTheZone(t *testing.T) {
	fake, _, _ := cloudflareFixture(t)
	var out bytes.Buffer
	err := cloudflareRun([]string{"login", "--domain", "mesh.example.com"}, &out, strings.NewReader(cfToken+"\n"))
	if err == nil || !strings.Contains(err.Error(), "Read") || strings.Contains(err.Error(), cfToken) {
		t.Fatalf("got %v", err)
	}
	if err := cloudflareRun([]string{"login"}, &out, strings.NewReader(cfToken+"\n")); err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Fatalf("without a domain: %v", err)
	}
	fake.ZoneList = []map[string]any{{"id": cfZone, "name": "other.org", "account": map[string]any{"id": cfAccount}}}
	if err := cloudflareRun([]string{"login", "--domain", "mesh.example.com"}, &out, strings.NewReader(cfToken+"\n")); err == nil || !strings.Contains(err.Error(), "other.org") {
		t.Fatalf("unrelated zones: %v", err)
	}
}

func TestCloudflareDiscoverListsWhatTheTokenSees(t *testing.T) {
	fake, _, _ := cloudflareFixture(t)
	fake.AccountList = []map[string]any{{"id": cfAccount, "name": "Me"}}
	fake.ZoneList = []map[string]any{{"id": cfZone, "name": "Example.com", "account": map[string]any{"id": cfAccount}}}
	var out bytes.Buffer
	if err := cloudflareRun([]string{"discover", "--json"}, &out, strings.NewReader(cfToken+"\n")); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"name":"example.com"`) || !strings.Contains(got, `"name":"Me"`) || !strings.Contains(got, cfZone) || strings.Contains(got, cfToken) {
		t.Fatalf("got %s", got)
	}
}
