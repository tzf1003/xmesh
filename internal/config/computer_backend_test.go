package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestComputerBackendNamespaceIsReserved(t *testing.T) {
	raw, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	agent := doc["agent"].(map[string]any)
	backend := map[string]any{"transport": "stdio", "command": []string{"fixture"}, "policy": "shared"}
	for _, name := range []string{"computer", "computer-helper"} {
		agent["backends"] = map[string]any{name: backend}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Parse(data)
		if (err != nil) != (name == "computer") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestBackendNameCannotShareABuiltinToolGroup(t *testing.T) {
	raw, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	agent := doc["agent"].(map[string]any)
	for _, name := range []string{"fs", "exec", "pty", "sys", "backend", "computer", "fs2"} {
		agent["backends"] = map[string]any{name: map[string]any{"transport": "stdio", "command": []string{"fixture"}, "policy": "shared"}}
		data, _ := json.Marshal(doc)
		_, err = Parse(data)
		if (err != nil) != (name != "fs2") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestBackendDescLimit(t *testing.T) {
	raw, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	agent := doc["agent"].(map[string]any)
	for n, want := range map[int]bool{200: true, 201: false} {
		agent["backends"] = map[string]any{"ida": map[string]any{"transport": "stdio", "command": []string{"fixture"}, "policy": "shared", "desc": strings.Repeat("说", n)}}
		data, _ := json.Marshal(doc)
		if _, err = Parse(data); (err == nil) != want {
			t.Fatalf("%d: %v", n, err)
		}
	}
}
