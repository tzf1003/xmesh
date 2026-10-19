package config

import (
	"encoding/json"
	"os"
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
