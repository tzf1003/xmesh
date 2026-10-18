package config

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func Test_DisabledToolsParseKnownNames(t *testing.T) {
	raw, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["agent"].(map[string]any)["disabled_tools"] = []string{"exec.run", "pty.open"}
	out, _ := json.Marshal(doc)
	c, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Agent.DisabledTools, []string{"exec.run", "pty.open"}) {
		t.Fatal(c.Agent.DisabledTools)
	}
	doc["agent"].(map[string]any)["disabled_tools"] = []string{"exec.nope"}
	if out, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(out); err == nil {
		t.Fatal("unknown built-in tool name accepted")
	}
}
