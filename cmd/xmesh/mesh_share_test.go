package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/meshadmin"
)

const tailnetShareConfig = "schema_version: 2\nnode:\n  name: laptop\n  roles: [agent]\nmesh:\n  lan:\n    enabled: false\n  tailnet:\n    enabled: true\n    control_url: %s\n    authkey_file: %s\nagent:\n  cwd_policy:\n    allow: []\n"

type tailnetShare struct {
	node    *fakeShareNode
	cfg     string
	keyFile string
	made    []meshadmin.AuthKeyRequest
	admin   bool
}

func newTailnetShare(t *testing.T, controlURL string, admin bool) *tailnetShare {
	t.Helper()
	dir := t.TempDir()
	s := &tailnetShare{node: &fakeShareNode{trusted: map[string]string{shareDevice: "phone"}}, admin: admin, keyFile: filepath.Join(dir, "local-key"), cfg: filepath.Join(dir, "xmesh.yaml")}
	if err := os.WriteFile(s.keyFile, []byte("tskey-auth-LOCAL-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(tailnetShareConfig, "%s", controlURL, 1)
	body = strings.Replace(body, "%s", s.keyFile, 1)
	if err := os.WriteFile(s.cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	oldNode, oldKey, oldAdmin := openShareNode, createShareKey, shareAdmin
	openShareNode = func(string, string) (shareNode, error) { return s.node, nil }
	createShareKey = func(_ *meshCommon, req meshadmin.AuthKeyRequest) (meshadmin.AuthKey, error) {
		s.made = append(s.made, req)
		return meshadmin.AuthKey{ID: "k1", Secret: "tskey-auth-GENERATED-0123456789"}, nil
	}
	shareAdmin = func(*meshCommon, string) (bool, string) { return s.admin, "" }
	t.Cleanup(func() { openShareNode, createShareKey, shareAdmin = oldNode, oldKey, oldAdmin })
	return s
}

func (s *tailnetShare) run(extra ...string) (string, error) {
	var out bytes.Buffer
	err := meshShare(append([]string{"--device", "phone", "--config", s.cfg}, extra...), &out)
	return out.String(), err
}

func TestMeshSharePreviewMakesNoKeyAndSendsNothing(t *testing.T) {
	s := newTailnetShare(t, "https://controlplane.tailscale.com", true)
	out, err := s.run()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.made) != 0 || len(s.node.sent) != 0 || !strings.Contains(out, "加 --apply") {
		t.Fatalf("%d keys, %d offers, %q", len(s.made), len(s.node.sent), out)
	}
}

func TestMeshShareGeneratesAKeyThroughTheAdminConnection(t *testing.T) {
	s := newTailnetShare(t, "https://controlplane.tailscale.com", true)
	out, err := s.run("--apply", "--tags", "tag:friend")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.made) != 1 || s.made[0].Reusable || s.made[0].TTL.Hours() != 48 || len(s.made[0].Tags) != 1 || s.made[0].Tags[0] != "tag:friend" {
		t.Fatalf("%+v", s.made)
	}
	if len(s.node.sent) != 1 || s.node.sentTo[0] != shareDevice {
		t.Fatalf("%+v", s.node.sent)
	}
	o := s.node.sent[0]
	if o.Kind != "tailnet" || o.Tailnet.AuthKey != "tskey-auth-GENERATED-0123456789" || o.Tailnet.ControlURL != "" {
		t.Fatalf("%+v", o.Tailnet)
	}
	if strings.Contains(out, o.Tailnet.AuthKey) {
		t.Fatalf("the output shows the key: %s", out)
	}
}

func TestMeshShareKeepsAHeadscaleControlURL(t *testing.T) {
	s := newTailnetShare(t, "https://headscale.example.com", true)
	if _, err := s.run("--apply"); err != nil {
		t.Fatal(err)
	}
	if got := s.node.sent[0].Tailnet.ControlURL; got != "https://headscale.example.com" {
		t.Fatalf("%q", got)
	}
}

func TestMeshShareUsesAGivenKeyFileWithoutAdminAccess(t *testing.T) {
	s := newTailnetShare(t, "https://controlplane.tailscale.com", false)
	given := filepath.Join(t.TempDir(), "given")
	_ = os.WriteFile(given, []byte("tskey-auth-GIVEN-0123456789\n"), 0o600)
	if _, err := s.run("--apply", "--authkey-file", given, "--ephemeral"); err != nil {
		t.Fatal(err)
	}
	o := s.node.sent[0].Tailnet
	if len(s.made) != 0 || o.AuthKey != "tskey-auth-GIVEN-0123456789" || !o.Ephemeral {
		t.Fatalf("%d keys, %+v", len(s.made), o)
	}
}

func TestMeshShareReusesTheLocalKeyOnlyWhenAsked(t *testing.T) {
	s := newTailnetShare(t, "https://controlplane.tailscale.com", false)
	_, err := s.run("--apply")
	if err == nil || !strings.Contains(err.Error(), "https://login.tailscale.com/admin/settings/keys") || len(s.node.sent) != 0 {
		t.Fatalf("no key source must prompt with the generation page: %v", err)
	}
	if _, err := s.run("--apply", "--reuse-local-key"); err != nil {
		t.Fatal(err)
	}
	if s.node.sent[0].Tailnet.AuthKey != "tskey-auth-LOCAL-0123456789" {
		t.Fatalf("%+v", s.node.sent[0].Tailnet)
	}
}

func TestMeshShareAsksForTheHeadscaleCommandWhenNothingCanMakeAKey(t *testing.T) {
	s := newTailnetShare(t, "https://headscale.example.com", false)
	_, err := s.run("--apply")
	if err == nil || !strings.Contains(err.Error(), "headscale preauthkeys create") {
		t.Fatalf("%v", err)
	}
}

func TestMeshSharePassesTheHeadscaleUserAndTrimsTheControlURLSlash(t *testing.T) {
	s := newTailnetShare(t, "https://headscale.example.com/", true)
	if _, err := s.run("--apply", "--user", "alice"); err != nil {
		t.Fatal(err)
	}
	if s.made[0].User != "alice" || s.node.sent[0].Tailnet.ControlURL != "https://headscale.example.com" {
		t.Fatalf("%+v %+v", s.made[0], s.node.sent[0].Tailnet)
	}
}

func TestAdminCredentialMustManageTheSameControlPlane(t *testing.T) {
	hs := meshadmin.Credential{Kind: meshadmin.KindHeadscale, URL: "https://headscale.example.com"}
	ts := meshadmin.Credential{Kind: meshadmin.KindTailscale}
	for _, tc := range []struct {
		cred meshadmin.Credential
		url  string
		want bool
	}{
		{hs, "https://headscale.example.com/", true},
		{hs, "https://other.example.com", false},
		{hs, "https://controlplane.tailscale.com", false},
		{ts, "https://controlplane.tailscale.com", true},
		{ts, "https://headscale.example.com", false},
	} {
		if got := sameControlPlane(tc.cred, tc.url); got != tc.want {
			t.Errorf("%s vs %s: %v", tc.cred.Kind, tc.url, got)
		}
	}
}

func TestMeshShareFailsWithoutSendingWhenTheAdminCannotMakeAKey(t *testing.T) {
	s := newTailnetShare(t, "https://controlplane.tailscale.com", true)
	createShareKey = func(*meshCommon, meshadmin.AuthKeyRequest) (meshadmin.AuthKey, error) {
		return meshadmin.AuthKey{}, errors.New("insufficient_scope")
	}
	_, err := s.run("--apply")
	if err == nil || !strings.Contains(err.Error(), "insufficient_scope") || len(s.node.sent) != 0 {
		t.Fatalf("%v %d", err, len(s.node.sent))
	}
}

func TestMeshShareNeedsAnEnabledTailnetAndAPairedDevice(t *testing.T) {
	s := newTailnetShare(t, "https://controlplane.tailscale.com", true)
	s.node.trusted = map[string]string{shareDevice: "tablet"}
	if _, err := s.run("--apply"); err == nil || len(s.made) != 0 {
		t.Fatalf("shared with an unpaired device: %v, %d keys", err, len(s.made))
	}
	body, _ := os.ReadFile(s.cfg)
	_ = os.WriteFile(s.cfg, []byte(strings.Replace(string(body), "enabled: true\n    control_url", "enabled: false\n    control_url", 1)), 0o600)
	if _, err := s.run("--apply"); err == nil {
		t.Fatal("shared a tailnet this node is not on")
	}
}
