package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// delegateDoc returns the example configuration with its agent section changed by edit.
func delegateDoc(t *testing.T, edit func(agent map[string]any)) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(doc["agent"].(map[string]any))
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_DelegateDefaults(t *testing.T) {
	for name, doc := range map[string][]byte{
		"section absent": delegateDoc(t, nil),
		"section empty": delegateDoc(t, func(agent map[string]any) {
			agent["delegate"] = map[string]any{}
		}),
	} {
		c, err := Parse(doc)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d := c.Agent.Delegate
		if d.Enabled || d.AllowWrite || !d.SessionsAllowed() || d.Concurrency() != 2 ||
			d.DefaultTimeoutDuration() != 30*time.Minute || d.MaxTimeoutDuration() != 2*time.Hour || d.RetainDuration() != time.Hour {
			t.Fatalf("%s: defaults = %+v", name, d)
		}
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_DelegateOverridesParse(t *testing.T) {
	c, err := Parse(delegateDoc(t, func(agent map[string]any) {
		agent["delegate"] = map[string]any{
			"enabled":        true,
			"allow_sessions": false,
			"max_concurrent": 4,
			"max_timeout":    "3h",
			"runners": map[string]any{
				"claude": map[string]any{"cmd": "/usr/local/bin/claude"},
				"mytool": map[string]any{"cmd": "mytool", "args": []string{"run"}},
			},
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := c.Agent.Delegate
	if !d.Enabled || d.SessionsAllowed() || d.Concurrency() != 4 || d.MaxTimeoutDuration() != 3*time.Hour || d.DefaultTimeoutDuration() != 30*time.Minute {
		t.Fatalf("overrides = %+v", d)
	}
	if d.Runners["claude"].Cmd != "/usr/local/bin/claude" || d.Runners["mytool"].Cmd != "mytool" {
		t.Fatalf("runners = %+v", d.Runners)
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_DelegateValuesAreValidated(t *testing.T) {
	cases := map[string]func(delegate map[string]any){
		"max_timeout below default_timeout": func(d map[string]any) { d["default_timeout"] = "2h"; d["max_timeout"] = "30m" },
		"unparseable max_timeout":           func(d map[string]any) { d["max_timeout"] = "soon" },
		"zero default_timeout":              func(d map[string]any) { d["default_timeout"] = "0s" },
		"negative retain":                   func(d map[string]any) { d["retain"] = "-1s" },
		"negative max_concurrent":           func(d map[string]any) { d["max_concurrent"] = -1 },
		"zero max_concurrent":               func(d map[string]any) { d["max_concurrent"] = 0 },
		"runner name with a space":          func(d map[string]any) { d["runners"] = map[string]any{"my tool": map[string]any{"cmd": "x"}} },
		"new runner without cmd": func(d map[string]any) {
			d["runners"] = map[string]any{"mytool": map[string]any{"args": []string{"-x"}}}
		},
	}
	for name, edit := range cases {
		doc := delegateDoc(t, func(agent map[string]any) {
			delegate := map[string]any{}
			edit(delegate)
			agent["delegate"] = delegate
		})
		if _, err := Parse(doc); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_DelegateIsAReservedBackendName(t *testing.T) {
	doc := delegateDoc(t, func(agent map[string]any) {
		agent["backends"] = map[string]any{"delegate": map[string]any{"transport": "stdio", "command": []string{"x"}, "policy": "shared"}}
	})
	if _, err := Parse(doc); err == nil {
		t.Fatal("a backend named delegate was accepted")
	}
}
