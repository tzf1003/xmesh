package config

import (
	"encoding/json"
	"testing"
)

func TestBackendLifecycleConfiguration(t *testing.T) {
	for _, input := range []string{
		`{"transport":"stdio","command":["node"],"policy":"per-caller","shutdown_timeout":"10s","process_cleanup":"tree"}`,
		`{"transport":"stdio","command":["node"],"policy":"shared"}`,
	} {
		var b Backend
		if err := json.Unmarshal([]byte(input), &b); err != nil {
			t.Fatal(err)
		}
		c, err := Load("../../docs/contracts/schemas/examples/xmesh.config.json")
		if err != nil {
			t.Fatal(err)
		}
		c.Agent.Backends = map[string]Backend{"ui": b}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		var again Backend
		_ = json.Unmarshal(out, &again)
		if again.ShutdownTimeout != b.ShutdownTimeout || again.ProcessCleanup != b.ProcessCleanup {
			t.Fatal("lifecycle settings lost")
		}
	}
	for _, b := range []Backend{
		{Transport: "stdio", ProcessCleanup: "all"},
		{Transport: "stdio", ShutdownTimeout: "0s"},
		{Transport: "stdio", ShutdownTimeout: "31s"},
		{Transport: "http", URL: "http://127.0.0.1:8000/mcp", ProcessCleanup: "tree"},
	} {
		c := Config{}
		c.Agent.Backends = map[string]Backend{"ui": b}
		if c.Validate() == nil {
			t.Fatalf("accepted %+v", b)
		}
	}
}
