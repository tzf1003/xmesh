package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreferredIPValidation(t *testing.T) {
	id := "dev-" + strings.Repeat("a", 64)
	file := filepath.Join(t.TempDir(), "ips")
	ok := func(m map[string]any) map[string]any { return m }
	cases := map[string]struct {
		pp   map[string]any
		fail bool
	}{
		"empty":             {ok(map[string]any{}), false},
		"smart with all":    {ok(map[string]any{"mode": "smart", "attempt_timeout": "4s", "cooldown": "30m", "fail_threshold": 3, "sources": []any{map[string]any{"type": "manual", "ips": []string{"104.16.0.1"}}, map[string]any{"type": "file", "path": file}, map[string]any{"type": "url", "url": "https://example.com/ips.txt", "refresh": "12h"}}, "scan": map[string]any{"enabled": true, "interval": "6h", "sample": 200, "keep": 8, "host": "win.link.example.com"}, "peers": map[string]any{id: map[string]any{"mode": "fixed", "ips": []string{"104.16.0.2"}}}}), false},
		"bad mode":          {ok(map[string]any{"mode": "round-robin"}), true},
		"fixed without ips": {ok(map[string]any{"mode": "fixed"}), true},
		"foreign fixed ip":  {ok(map[string]any{"mode": "fixed", "fixed_ips": []string{"8.8.8.8"}}), true},
		"slow attempt":      {ok(map[string]any{"attempt_timeout": "5m"}), true},
		"short cooldown":    {ok(map[string]any{"cooldown": "5s"}), true},
		"bad threshold":     {ok(map[string]any{"fail_threshold": 99}), true},
		"unknown source":    {ok(map[string]any{"sources": []any{map[string]any{"type": "dns"}}}), true},
		"relative file":     {ok(map[string]any{"sources": []any{map[string]any{"type": "file", "path": "ips"}}}), true},
		"http url":          {ok(map[string]any{"sources": []any{map[string]any{"type": "url", "url": "http://example.com/ips"}}}), true},
		"url with user":     {ok(map[string]any{"sources": []any{map[string]any{"type": "url", "url": "https://u:p@example.com/ips"}}}), true},
		"fast refresh":      {ok(map[string]any{"sources": []any{map[string]any{"type": "url", "url": "https://example.com/ips", "refresh": "1s"}}}), true},
		"manual with url":   {ok(map[string]any{"sources": []any{map[string]any{"type": "manual", "url": "https://example.com"}}}), true},
		"foreign manual":    {ok(map[string]any{"sources": []any{map[string]any{"type": "manual", "ips": []string{"1.1.1.1"}}}}), true},
		"too many sources":  {ok(map[string]any{"sources": make([]any, 9)}), true},
		"fast scan":         {ok(map[string]any{"scan": map[string]any{"interval": "1m"}}), true},
		"huge sample":       {ok(map[string]any{"scan": map[string]any{"sample": 100000}}), true},
		"bad scan host":     {ok(map[string]any{"scan": map[string]any{"host": "not a host"}}), true},
		"bad peer id":       {ok(map[string]any{"peers": map[string]any{"win": map[string]any{"mode": "off"}}}), true},
		"bad peer mode":     {ok(map[string]any{"peers": map[string]any{id: map[string]any{"mode": "auto"}}}), true},
		"peer off":          {ok(map[string]any{"peers": map[string]any{id: map[string]any{"mode": "off"}}}), false},
	}
	for name, c := range cases {
		if s, bad := c.pp["sources"].([]any); bad && len(s) == 9 {
			for i := range s {
				s[i] = map[string]any{"type": "manual"}
			}
		}
		doc := map[string]any{"schema_version": 2, "node": map[string]any{"name": "n", "roles": []string{"agent"}}, "mesh": map[string]any{"cloudflare": map[string]any{"enabled": true, "preferred_ip": c.pp}}, "agent": map[string]any{"cwd_policy": map[string]any{"allow": []string{}}}}
		raw, _ := json.Marshal(doc)
		_, err := Parse(raw)
		if (err != nil) != c.fail {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestPeerModeFollowsOverrides(t *testing.T) {
	a, b, c := "dev-"+strings.Repeat("a", 64), "dev-"+strings.Repeat("b", 64), "dev-"+strings.Repeat("c", 64)
	off := false
	cf := Cloudflare{PreferredIP: &PreferredIP{Mode: "fixed", FixedIPs: []string{"104.16.0.1"}, Peers: map[string]PreferredPeer{
		a: {Mode: "smart"}, b: {Mode: "off"}, c: {Mode: "default"},
	}}, Peers: map[string]string{a: "a.example.com", b: "b.example.com", c: "c.example.com"}}
	for id, want := range map[string]string{a: "smart", b: "off", c: "fixed", "dev-" + strings.Repeat("d", 64): "fixed"} {
		if got, _ := cf.PeerMode(id); got != want {
			t.Errorf("%s: %s, want %s", id[:6], got, want)
		}
	}
	if m, ips := cf.PeerMode(c); m != "fixed" || len(ips) != 1 {
		t.Fatal(m, ips)
	}
	if !cf.PeerOverridden(a) || cf.PeerOverridden(c) {
		t.Fatal("override flags")
	}
	if cf.PeerForHost("B.example.com") != b || cf.PeerForHost("x.example.com") != "" {
		t.Fatal("host lookup")
	}
	cf.PreferredIP.Enabled = &off
	if m, _ := cf.PeerMode(a); m != "off" || cf.PreferredOn() {
		t.Fatal("the master switch wins over overrides")
	}
	if !(Cloudflare{}).PreferredOn() {
		t.Fatal("on by default")
	}
}

func TestPreferredIPEditsNeedNoRestart(t *testing.T) {
	for _, p := range []string{"/mesh/cloudflare/preferred_ip", "/mesh/cloudflare/preferred_ip/sources/0", "/mesh/cloudflare/preferred_ips", "/mesh/cloudflare/preferred_ips_file"} {
		if NeedsRestart(p) {
			t.Errorf("%s needs a restart", p)
		}
	}
	if !NeedsRestart("/mesh/cloudflare/hostname") {
		t.Error("hostname still needs a restart")
	}
}
