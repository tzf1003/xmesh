package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// serviceFixture is shared by local schema checks and native service acceptance.
func serviceFixture(t *testing.T, name, root string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"node": map[string]any{"name": name, "roles": []string{"agent"}},
		"mesh": map[string]any{"mode": "lan", "lan": map[string]any{"mdns": true}},
		"agent": map[string]any{
			"default_cwd": root,
			"cwd_policy":  map[string]any{"allow": []string{root}},
			"authz":       map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Parse(body); err != nil {
		t.Fatal("service acceptance configuration", err)
	}
	return body
}

func Test_ServiceAcceptanceConfiguration(t *testing.T) {
	serviceFixture(t, "service-validation", filepath.Join(t.TempDir(), "service space & 中文"))
}
