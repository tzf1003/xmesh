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
	joinOn   bool // a new device appears once the password is set
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
	return nil
}
func (f *fakeInviteNode) Trusted() (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.joinOn && f.password != "" {
		return map[string]bool{"dev-" + strings.Repeat("cd", 32): true}, nil
	}
	return map[string]bool{}, nil
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
