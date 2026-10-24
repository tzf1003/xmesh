package config

import (
	"strings"
	"testing"
)

func TestConnectivityV2ModesAndValidation(t *testing.T) {
	prefix := `{"schema_version":2,"node":{"name":"device","roles":["agent"]},"agent":{"cwd_policy":{"allow":[]}},"mesh":`
	for _, test := range []struct {
		mesh, mode string
		valid      bool
	}{
		{`{}`, "lan", true},
		{`{"lan":{"enabled":false}}`, "offline", true},
		{`{"tailnet":{"enabled":true,"control_url":"https://controlplane.tailscale.com","auth_method":"browser"}}`, "coexist", true},
		{`{"lan":{"enabled":false},"tailnet":{"enabled":true,"control_url":"https://hs.example.com","authkey_file":"/key"}}`, "tailnet", true},
		{`{"lan":{"enabled":false},"cloudflare":{"enabled":true}}`, "cloudflare", true},
		{`{"cloudflare":{"enabled":true}}`, "coexist", true},
		{`{"lan":{"enabled":false},"tailnet":{"enabled":true,"control_url":"https://hs.example.com","authkey_file":"/key"},"cloudflare":{"enabled":true}}`, "coexist", true},
		{`{"tailnet":{"enabled":true}}`, "", false},
		{`{"mode":"lan","lan":{"mdns":true}}`, "", false},
		{`{"tailnet":{"enabled":true,"control_url":"https://hs.example.com","auth_method":"browser","ephemeral":true}}`, "", false},
	} {
		cfg, err := Parse([]byte(prefix + test.mesh + "}"))
		if (err == nil) != test.valid {
			t.Errorf("%s valid=%v err=%v", test.mesh, test.valid, err)
			continue
		}
		if err == nil && cfg.Mesh.NetworkMode() != test.mode {
			t.Error(cfg.Mesh.NetworkMode(), test.mode)
		}
		if err == nil && !strings.Contains(test.mesh, `"mdns"`) && !cfg.Mesh.LAN.MDNS {
			t.Error("mDNS default not applied")
		}
	}
}
