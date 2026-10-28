package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDirectValidation(t *testing.T) {
	id := "dev-" + strings.Repeat("a", 64)
	cases := map[string]struct {
		direct map[string]any
		fail   bool
	}{
		"defaults":                     {map[string]any{"enabled": true}, false},
		"advertise and peers":          {map[string]any{"enabled": true, "advertise": "mesh.example.com:7435", "peers": map[string]string{id: "203.0.113.5:7435"}}, false},
		"ipv6 peer":                    {map[string]any{"enabled": true, "peers": map[string]string{id: "[2001:db8::1]:7435"}}, false},
		"listen without port":          {map[string]any{"enabled": true, "listen": "0.0.0.0"}, true},
		"listen port zero":             {map[string]any{"enabled": true, "listen": "0.0.0.0:0"}, true},
		"advertise with scheme":        {map[string]any{"enabled": true, "advertise": "https://mesh.example.com:7435"}, true},
		"peer without port":            {map[string]any{"enabled": true, "peers": map[string]string{id: "203.0.113.5"}}, true},
		"bad peer id":                  {map[string]any{"enabled": true, "peers": map[string]string{"win": "203.0.113.5:7435"}}, true},
		"duplicate peer address":       {map[string]any{"enabled": true, "peers": map[string]string{id: "Host.example.com:1", "dev-" + strings.Repeat("b", 64): "host.example.com:1"}}, true},
		"unknown field":                {map[string]any{"enabled": true, "hostname": "x"}, true},
		"not validated while disabled": {map[string]any{"enabled": false, "listen": "bad"}, false},
	}
	for name, c := range cases {
		doc := map[string]any{"schema_version": 2, "node": map[string]any{"name": "n", "roles": []string{"agent"}}, "mesh": map[string]any{"direct": c.direct}, "agent": map[string]any{"cwd_policy": map[string]any{"allow": []string{}}}}
		raw, _ := json.Marshal(doc)
		cfg, err := Parse(raw)
		if (err != nil) != c.fail {
			t.Errorf("%s: err=%v", name, err)
		}
		if err == nil && c.direct["enabled"] == true && cfg.Mesh.NetworkMode() != "coexist" {
			t.Errorf("%s: mode %s", name, cfg.Mesh.NetworkMode())
		}
	}
}

func TestDirectListenDefault(t *testing.T) {
	if got := (Direct{}).ListenAddr(); got != "0.0.0.0:7435" {
		t.Fatal(got)
	}
}
