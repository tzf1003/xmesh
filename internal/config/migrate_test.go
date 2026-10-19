package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDeviceMigrationPreservesPolicyAndProviderChoice(t *testing.T) {
	for _, network := range []string{"mode: lan\n  lan:\n    mdns: true", "mode: tailnet\n  control_url: https://controlplane.tailscale.com\n  auth_method: browser"} {
		raw := []byte("# preserve this policy\nnode:\n  name: device\n  roles: [agent]\nmesh:\n  " + network + "\nagent:\n  cwd_policy:\n    allow: [/work]\n  policy:\n    allow_dangerous: false # preserve this choice\n")
		before, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := MigrateDeviceConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		after, err := Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if after.SchemaVersion != 2 || after.Mesh.NetworkMode() != before.Mesh.Mode || after.Node.Name != before.Node.Name || after.Agent.Policy.AllowDangerous != before.Agent.Policy.AllowDangerous {
			t.Fatal("migration changed provider or policy", string(got))
		}
		if !strings.Contains(string(got), "# preserve this choice") || !strings.Contains(string(got), "# preserve this policy") {
			t.Fatal("comments lost")
		}
		again, err := MigrateDeviceConfig(got)
		if err != nil || !bytes.Equal(again, got) {
			t.Fatal("migration not idempotent", err)
		}
	}
}

func TestDeviceMigrationKeepsJSONAsJSON(t *testing.T) {
	raw := []byte(`{"node":{"name":"json-node","roles":["agent"]},"mesh":{"mode":"lan","lan":{"mdns":true}},"agent":{"cwd_policy":{"allow":[]}}}`)
	got, err := MigrateDeviceConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) {
		t.Fatal("JSON configuration changed format", string(got))
	}
}

func TestDeviceMigrationPreservesLocalTailnetGrant(t *testing.T) {
	for _, existing := range []string{"", "\n    tag:peer:\n      tools: [fs.stat]\n      backends: []"} {
		raw := []byte("node:\n  name: old-node\n  roles: [agent]\nmesh:\n  mode: tailnet\n  control_url: https://controlplane.tailscale.com\n  auth_method: browser\nagent:\n  cwd_policy:\n    allow: []\n  authz:\n    tag:device:\n      tools: [sys.info]\n      backends: []" + existing + "\n")
		before, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		converted, err := MigrateDeviceConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		after, err := Parse(converted)
		if err != nil {
			t.Fatal(err)
		}
		want := before.Agent.Authz["tag:device"]
		if existing != "" {
			want = before.Agent.Authz["tag:peer"]
		}
		if !reflect.DeepEqual(after.Agent.Authz["tag:peer"], want) || !reflect.DeepEqual(after.Agent.Authz["tag:device"], before.Agent.Authz["tag:device"]) {
			t.Fatal("local grant changed", string(converted))
		}
	}
}

func TestDeviceMigrationPreservesAliasedTailnetGrant(t *testing.T) {
	raw := []byte(`
node: {name: old-node, roles: [agent]}
mesh: {mode: tailnet, control_url: https://controlplane.tailscale.com, auth_method: browser}
<<: &defaults
  agent:
    authz: &shared-authz
      tag:device: {tools: [sys.info], backends: []}
agent:
  cwd_policy: {allow: []}
  authz: *shared-authz
`)
	before, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := MigrateDeviceConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse(converted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Agent.Authz["tag:peer"], before.Agent.Authz["tag:device"]) {
		t.Fatal("aliased authorization lost", string(converted))
	}
}
