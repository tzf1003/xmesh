package config

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPathSelectionOrder(t *testing.T) {
	cases := map[string]struct {
		sel  PathSelection
		want []string
	}{
		"default":                   {PathSelection{}, []string{"lan", "tailnet", "cloudflare"}},
		"listed first":              {PathSelection{Priority: []string{"cloudflare", "lan"}}, []string{"cloudflare", "lan", "tailnet"}},
		"unlisted keep their order": {PathSelection{Priority: []string{"tailnet"}}, []string{"tailnet", "lan", "cloudflare"}},
		"disabled dropped":          {PathSelection{Priority: []string{"cloudflare"}, Disabled: []string{"tailnet"}}, []string{"cloudflare", "lan"}},
		"disabled listed":           {PathSelection{Priority: []string{"tailnet", "lan"}, Disabled: []string{"tailnet"}}, []string{"lan", "cloudflare"}},
		"all disabled":              {PathSelection{Disabled: []string{"lan", "tailnet", "cloudflare"}}, []string{}},
	}
	for name, c := range cases {
		if got := c.sel.Order(); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestPathSelectionValidation(t *testing.T) {
	cases := map[string]struct {
		sel  map[string]any
		fail bool
	}{
		"smart":           {map[string]any{"mode": "smart", "disabled": []string{"tailnet"}}, false},
		"manual":          {map[string]any{"mode": "manual", "priority": []string{"cloudflare", "lan"}, "disabled": []string{"tailnet"}}, false},
		"empty":           {map[string]any{}, false},
		"unknown mode":    {map[string]any{"mode": "fastest"}, true},
		"unknown path":    {map[string]any{"priority": []string{"bluetooth"}}, true},
		"duplicate":       {map[string]any{"priority": []string{"lan", "lan"}}, true},
		"unknown dis":     {map[string]any{"disabled": []string{"wifi"}}, true},
		"duplicate dis":   {map[string]any{"disabled": []string{"lan", "lan"}}, true},
		"extra key":       {map[string]any{"weights": map[string]any{}}, true},
		"priority a name": {map[string]any{"priority": "lan"}, true},
	}
	for name, c := range cases {
		doc := map[string]any{"schema_version": 2, "node": map[string]any{"name": "n", "roles": []string{"agent"}}, "mesh": map[string]any{"path_selection": c.sel}, "agent": map[string]any{"cwd_policy": map[string]any{"allow": []string{}}}}
		raw, _ := json.Marshal(doc)
		if _, err := Parse(raw); (err != nil) != c.fail {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestPathSelectionNeedsNoRestart(t *testing.T) {
	for _, ptr := range []string{"/mesh/path_selection", "/mesh/path_selection/priority", "/mesh/path_selection/disabled"} {
		if NeedsRestart(ptr) {
			t.Errorf("%q should apply without a restart", ptr)
		}
	}
	if !NeedsRestart("/mesh") {
		t.Error("replacing the whole mesh section still needs a restart")
	}
}

func TestPathSelectionDefaultsToSmart(t *testing.T) {
	for mode, want := range map[string]bool{"": true, "smart": true, "manual": false} {
		if got := (PathSelection{Mode: mode}).Smart(); got != want {
			t.Errorf("mode %q: smart = %v", mode, got)
		}
	}
}
