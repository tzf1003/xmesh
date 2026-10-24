package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/cfadmin"
	"github.com/tzf1003/xmesh/internal/config"
)

type fakeInviteNode struct {
	mu       sync.Mutex
	password string
	cleared  int
	clearErr error
	joinOn   bool   // a new device appears once the password is set
	joiner   string // its name; defaults to "phone"
}

func (f *fakeInviteNode) Fingerprint(context.Context) (string, error) {
	return strings.Repeat("ab", 32), nil
}
func (f *fakeInviteNode) SetPassword(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = p
	return nil
}
func (f *fakeInviteNode) ClearPassword(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared++
	f.password = ""
	return f.clearErr
}
func (f *fakeInviteNode) Trusted() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.joinOn && f.password != "" {
		name := f.joiner
		if name == "" {
			name = "phone"
		}
		return map[string]string{"dev-" + strings.Repeat("cd", 32): name}, nil
	}
	return map[string]string{}, nil
}

func inviteFixture(t *testing.T, node *fakeInviteNode) (fakeThings func() int, cfg string, args []string) {
	t.Helper()
	fake, cfgPath, state := cloudflareFixture(t)
	cloudflareLogin_(t)
	body := "schema_version: 2\nnode:\n  name: laptop\n  roles: [agent]\nmesh:\n  lan:\n    enabled: false\n  cloudflare:\n    enabled: true\n    hostname: xmesh-laptop.mesh.example.com\nagent:\n  cwd_policy:\n    allow: []\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old, oldPoll := openInviteNode, invitePoll
	openInviteNode = func(string, string) (inviteNode, error) { return node, nil }
	invitePoll = 5 * time.Millisecond
	t.Cleanup(func() { openInviteNode, invitePoll = old, oldPoll })
	return func() int { return len(fake.Things) }, cfgPath, append([]string{"invite", "--name", "phone", "--config", cfgPath}, state...)
}

func TestCloudflareInviteKeepsDeviceOnceItJoins(t *testing.T) {
	node := &fakeInviteNode{joinOn: true}
	things, _, args := inviteFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(append(args, "--apply"), &out, nil); err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`xmesh join (\S+)`).FindStringSubmatch(out.String())
	if code == nil {
		t.Fatalf("no code in %q", out.String())
	}
	inv, err := cfadmin.DecodeInvite(code[1], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if inv.Name != "phone" || inv.Admin != "xmesh-laptop.mesh.example.com" || inv.Fingerprint != strings.Repeat("ab", 32) || !strings.HasPrefix(inv.TunnelToken, "tunnel-token-") || !strings.HasPrefix(inv.AccessID, "cid-") {
		t.Fatalf("%+v", inv)
	}
	if node.cleared == 0 || node.password != "" {
		t.Fatal("pairing window left open")
	}
	if things() == 0 {
		t.Fatal("joined device's resources were removed")
	}
}

func TestCloudflareInviteExpiryRemovesTheDevice(t *testing.T) {
	node := &fakeInviteNode{}
	things, _, args := inviteFixture(t, node)
	var out bytes.Buffer
	err := cloudflareRun(append(args, "--apply", "--ttl", "60ms"), &out, nil)
	if err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("%v", err)
	}
	if things() != 0 {
		t.Fatal("expired invite left resources behind")
	}
	if node.password != "" || node.cleared == 0 {
		t.Fatal("pairing window left open")
	}
}

func TestCloudflareInvitePreviewTouchesNothing(t *testing.T) {
	node := &fakeInviteNode{}
	things, _, args := inviteFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(args, &out, nil); err != nil {
		t.Fatal(err)
	}
	if things() != 0 || node.password != "" {
		t.Fatal("preview changed something")
	}
}

func joinFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "xmesh.yaml")
	body := "# phone\nschema_version: 2\nnode:\n  name: phone\n  roles: [agent]\nmesh:\n  lan:\n    enabled: false\nagent:\n  cwd_policy:\n    allow: []\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := cfadmin.Invite{Version: 1, Name: "phone", Admin: "xmesh-laptop.mesh.example.com", Fingerprint: strings.Repeat("ab", 32), Password: "one-time-password", Hostname: "xmesh-phone.mesh.example.com", TunnelToken: "TUNNEL-SECRET", AccessID: "cid.access", AccessKey: "ACCESS-SECRET", Expires: time.Now().Add(time.Minute).Unix()}
	code, err := inv.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, code
}

func TestJoinWritesConfigAfterPairing(t *testing.T) {
	cfg, code := joinFixture(t)
	old := joinPair
	var used cfadmin.Invite
	joinPair = func(_ context.Context, c *config.Config, _ string, inv cfadmin.Invite) (string, error) {
		used = inv
		secret, err := os.ReadFile(c.Mesh.Cloudflare.AccessClientSecretFile)
		if err != nil || strings.TrimSpace(string(secret)) != "ACCESS-SECRET" {
			t.Errorf("pairing ran without the Access secret: %q %v", secret, err)
		}
		return "dev-" + inv.Fingerprint, nil
	}
	t.Cleanup(func() { joinPair = old })
	var out bytes.Buffer
	if err := joinRun([]string{code, "--config", cfg, "--state-dir", t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	if used.Password != "one-time-password" {
		t.Fatalf("%+v", used)
	}
	raw, _ := os.ReadFile(cfg)
	text := string(raw)
	for _, want := range []string{"# phone", "enabled: true", "hostname: xmesh-phone.mesh.example.com", "dev-" + strings.Repeat("ab", 32) + ": xmesh-laptop.mesh.example.com", "tunnel_token_file:"} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "SECRET") || strings.Contains(out.String(), "SECRET") || strings.Contains(out.String(), "one-time-password") {
		t.Fatal("secret leaked into config or output")
	}
	tunnel, err := os.ReadFile(filepath.Join(filepath.Dir(cfg), "cloudflare", "tunnel-token"))
	if err != nil || strings.TrimSpace(string(tunnel)) != "TUNNEL-SECRET" {
		t.Fatalf("%q %v", tunnel, err)
	}
}

func TestJoinFailureLeavesNothingBehind(t *testing.T) {
	cfg, code := joinFixture(t)
	before, _ := os.ReadFile(cfg)
	old := joinPair
	joinPair = func(context.Context, *config.Config, string, cfadmin.Invite) (string, error) {
		return "", errors.New("unreachable")
	}
	t.Cleanup(func() { joinPair = old })
	var out bytes.Buffer
	if err := joinRun([]string{code, "--config", cfg, "--state-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("join succeeded")
	}
	after, _ := os.ReadFile(cfg)
	if !bytes.Equal(before, after) {
		t.Fatal("config changed")
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(cfg), "cloudflare"))
	if len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestJoinRejectsExpiredAndGarbageCodes(t *testing.T) {
	cfg, _ := joinFixture(t)
	var out bytes.Buffer
	for _, code := range []string{"nonsense", cfadmin.InvitePrefix + "e30"} {
		err := joinRun([]string{code, "--config", cfg}, &out)
		if err == nil || strings.Contains(err.Error(), code) && len(code) > 12 {
			t.Errorf("%q: %v", code, err)
		}
	}
}

func TestCloudflareInviteIgnoresUnrelatedPairings(t *testing.T) {
	node := &fakeInviteNode{joinOn: true, joiner: "someone-else"}
	things, _, args := inviteFixture(t, node)
	var out bytes.Buffer
	err := cloudflareRun(append(args, "--apply", "--ttl", "80ms"), &out, nil)
	if err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("an unrelated pairing ended the invite: %v", err)
	}
	if things() != 0 {
		t.Fatal("resources of an unused invite were kept")
	}
}

func TestCloudflareInviteJSON(t *testing.T) {
	node := &fakeInviteNode{joinOn: true}
	_, _, args := inviteFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(append(args, "--apply", "--json"), &out, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"event":"invite"`) || !strings.Contains(lines[0], cfadmin.InvitePrefix) || !strings.Contains(lines[1], `"event":"joined"`) {
		t.Fatalf("%q", out.String())
	}
}

func TestJoinRejectsCodeForAnotherNodeName(t *testing.T) {
	cfg, code := joinFixture(t)
	raw, _ := os.ReadFile(cfg)
	if err := os.WriteFile(cfg, bytes.Replace(raw, []byte("name: phone"), []byte("name: tablet"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := joinRun([]string{code, "--config", cfg, "--state-dir", t.TempDir()}, &out); err == nil || !strings.Contains(err.Error(), "节点名") {
		t.Fatalf("%v", err)
	}
}

func TestJoinCleansUpSecretsWhenSetupFailsAfterPairing(t *testing.T) {
	cfg, code := joinFixture(t)
	secrets := filepath.Join(filepath.Dir(cfg), "cloudflare")
	old := joinPair
	joinPair = func(_ context.Context, _ *config.Config, _ string, inv cfadmin.Invite) (string, error) {
		// A directory where the tunnel token goes makes the write after pairing fail.
		return "dev-" + inv.Fingerprint, os.MkdirAll(filepath.Join(secrets, "tunnel-token"), 0o700)
	}
	t.Cleanup(func() { joinPair = old })
	var out bytes.Buffer
	if err := joinRun([]string{code, "--config", cfg, "--state-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("join succeeded")
	}
	if _, err := os.Stat(filepath.Join(secrets, "access-secret")); !os.IsNotExist(err) {
		t.Fatal("the Access secret was left behind")
	}
}

func TestCloudflareInviteRejectsNamesJoinCannotUse(t *testing.T) {
	node := &fakeInviteNode{}
	things, _, args := inviteFixture(t, node)
	args[2] = "my.phone"
	var out bytes.Buffer
	if err := cloudflareRun(append(args, "--apply"), &out, nil); err == nil {
		t.Fatal("accepted a name no node can have")
	}
	if things() != 0 {
		t.Fatal("created resources for it")
	}
}

func TestJoinRejectsNonDefaultListener(t *testing.T) {
	cfg, code := joinFixture(t)
	raw, _ := os.ReadFile(cfg)
	raw = bytes.Replace(raw, []byte("  lan:\n    enabled: false\n"), []byte("  lan:\n    enabled: false\n  cloudflare:\n    enabled: false\n    listen: 127.0.0.1:9000\n"), 1)
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := joinRun([]string{code, "--config", cfg, "--state-dir", t.TempDir()}, &out); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("%v", err)
	}
}

func TestCloudflareInviteReportsAWindowThatCouldNotBeClosed(t *testing.T) {
	node := &fakeInviteNode{joinOn: true, clearErr: errors.New("control unreachable")}
	_, _, args := inviteFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(append(args, "--apply"), &out, nil); err == nil || !strings.Contains(err.Error(), "配对窗口") {
		t.Fatalf("a window left open was reported as success: %v", err)
	}
}

func TestJoinKeepsExistingCloudflareCredentials(t *testing.T) {
	cfg, code := joinFixture(t)
	secret := filepath.Join(filepath.Dir(cfg), "mine")
	if err := os.WriteFile(secret, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cfg)
	raw = bytes.Replace(raw, []byte("  lan:\n    enabled: false\n"), []byte("  lan:\n    enabled: false\n  cloudflare:\n    enabled: true\n    access_client_id: old.access\n    access_client_secret_file: "+secret+"\n"), 1)
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := joinRun([]string{code, "--config", cfg, "--state-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("join went ahead over existing credentials")
	}
	if got, _ := os.ReadFile(secret); string(got) != "keep\n" {
		t.Fatal("existing secret was touched")
	}
}
