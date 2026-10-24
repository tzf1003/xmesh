package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflareValidation(t *testing.T) {
	id := "dev-" + strings.Repeat("a", 64)
	secret := filepath.Join(t.TempDir(), "secret")
	cases := map[string]struct {
		cf   map[string]any
		fail bool
	}{
		"dial only":                    {map[string]any{"enabled": true}, false},
		"reachable":                    {map[string]any{"enabled": true, "hostname": "mac.link.example.com", "access_client_id": "x.access", "access_client_secret_file": secret, "peers": map[string]string{id: "win.link.example.com"}}, false},
		"bad hostname":                 {map[string]any{"enabled": true, "hostname": "not a host"}, true},
		"public listen":                {map[string]any{"enabled": true, "listen": "0.0.0.0:7434"}, true},
		"id without file":              {map[string]any{"enabled": true, "access_client_id": "x.access"}, true},
		"relative secret":              {map[string]any{"enabled": true, "access_client_id": "x.access", "access_client_secret_file": "secret"}, true},
		"port zero":                    {map[string]any{"enabled": true, "listen": "127.0.0.1:0"}, true},
		"port too large":               {map[string]any{"enabled": true, "listen": "127.0.0.1:99999"}, true},
		"port not number":              {map[string]any{"enabled": true, "listen": "127.0.0.1:abc"}, true},
		"duplicate host":               {map[string]any{"enabled": true, "peers": map[string]string{id: "win.link.example.com", "dev-" + strings.Repeat("b", 64): "WIN.link.example.com"}}, true},
		"managed cloudflared":          {map[string]any{"enabled": true, "hostname": "mac.link.example.com", "tunnel_token_file": filepath.Join(t.TempDir(), "token"), "cloudflared": "cloudflared"}, false},
		"token file needs hostname":    {map[string]any{"enabled": true, "tunnel_token_file": filepath.Join(t.TempDir(), "token")}, true},
		"relative token file":          {map[string]any{"enabled": true, "hostname": "mac.link.example.com", "tunnel_token_file": "token"}, true},
		"binary without token file":    {map[string]any{"enabled": true, "hostname": "mac.link.example.com", "cloudflared": "cloudflared"}, true},
		"bad peer id":                  {map[string]any{"enabled": true, "peers": map[string]string{"win": "win.link.example.com"}}, true},
		"schema applies when disabled": {map[string]any{"enabled": false, "hostname": "not a host"}, true},
	}
	for name, c := range cases {
		doc := map[string]any{"schema_version": 2, "node": map[string]any{"name": "n", "roles": []string{"agent"}}, "mesh": map[string]any{"cloudflare": c.cf}, "agent": map[string]any{"cwd_policy": map[string]any{"allow": []string{}}}}
		raw, _ := json.Marshal(doc)
		_, err := Parse(raw)
		if (err != nil) != c.fail {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}
