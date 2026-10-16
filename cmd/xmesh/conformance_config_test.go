package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A configuration that fails schema validation or the role linkage rules makes
// `xmesh serve` return an error before anything is started, and the message
// names the offending location as a JSON Pointer.
// conformance: CTR-CFG-01
func Test_CTRCFG01_InvalidConfigurationRefusesToStart(t *testing.T) {
	invalid := filepath.Join("..", "..", "docs", "contracts", "schemas", "examples", "invalid")
	for name, want := range map[string]string{
		"config-relay.json":                  "/mesh/mode",
		"config-funnel-with-domain.json":     "/ingress",
		"config-pat-empty-scopes.json":       "/gateway/pats/reader/scopes",
		"config-lan-ephemeral.json":          "/mesh/ephemeral",
		"config-pty-authority-bad-name.json": "/ingress/pty_budget_authority",
	} {
		name, want := name, want
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- run([]string{"serve", "--config", filepath.Join(invalid, name)}) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("serve accepted an invalid configuration")
				}
				if !strings.Contains(err.Error(), "'"+want) {
					t.Errorf("the error does not name the JSON Pointer %s: %v", want, err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("serve kept running with an invalid configuration")
			}
		})
	}
	// The contract's own example location: a backend with an unknown policy.
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "schemas", "examples", "xmesh.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	agent := doc["agent"].(map[string]any)
	agent["backends"] = map[string]any{"ida": map[string]any{"transport": "stdio", "command": []string{"ida-mcp"}, "policy": "sometimes"}}
	body, _ = json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "bad-backend.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"serve", "--config", path}); err == nil || !strings.Contains(err.Error(), "/agent/backends/ida/policy") {
		t.Errorf("the error does not name /agent/backends/ida/policy: %v", err)
	}
	// Unparseable and missing files are refused too.
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(broken, []byte("agent: [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{broken, filepath.Join(t.TempDir(), "missing.yaml")} {
		if err := run([]string{"serve", "--config", path}); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Errorf("serve --config %s: %v", path, err)
		}
	}
}
