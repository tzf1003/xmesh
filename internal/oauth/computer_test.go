package oauth

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

func TestComputerScopesAppearInDiscovery(t *testing.T) {
	_, h := oauthFixture(t)
	get := func(path string) (int, http.Header, string) { return request(t, h, "GET", path, "", "") }
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		doc := getJSON(t, path, get)
		raw, _ := doc["scopes_supported"].([]any)
		for _, scope := range []string{"computer:observe:*", "computer:control:*", "computer:clipboard-read:*", "computer:clipboard-write:*"} {
			if !slices.Contains(raw, any(scope)) {
				t.Fatalf("%s does not advertise %s: %v", path, scope, raw)
			}
		}
	}
}

func TestComputerScopesAndMixedModeOpen(t *testing.T) {
	observe := []string{"computer:observe:mac"}
	control := []string{"computer:control:mac"}
	for _, s := range [][]string{observe, control, {"computer:clipboard-read:*"}, {"computer:clipboard-write:win"}} {
		if _, e := Expand(s); e != nil {
			t.Fatal(s, e)
		}
	}
	for _, s := range []string{"computer:*:*", "computer:admin:mac", "computer:control:", "computer:observe:bad/name"} {
		if _, e := Expand([]string{s}); e == nil {
			t.Fatal(s)
		}
	}
	if Allows(observe, "mac", "computer.act", "") || Allows(observe, "win", "computer.observe", "") {
		t.Fatal("scope escalation")
	}
	if !Allows(observe, "mac", "computer.open", "") || !Allows(control, "mac", "computer.observe", "") {
		t.Fatal("scope visibility")
	}
	for _, mode := range []string{"control", "observe"} {
		args := map[string]json.RawMessage{"mode": json.RawMessage(`"` + mode + `"`)}
		if AllowsArguments(observe, "mac", "computer.open", args) != (mode == "observe") {
			t.Fatal(mode)
		}
		if !AllowsArguments(control, "mac", "computer.open", args) {
			t.Fatal(mode)
		}
	}
	if Allows([]string{"computer:clipboard-read:mac"}, "mac", "computer.observe", "") {
		t.Fatal("clipboard scope exposed screen")
	}
}

func TestComputerScopesDoNotAuthorizeBackendTools(t *testing.T) {
	for _, tool := range []string{"computer.erase", "computer.clipboard", "computer.act_extra"} {
		for _, scopes := range [][]string{{"computer:observe:*"}, {"computer:control:*"}} {
			if Allows(scopes, "mac", tool, "") || Allows(scopes, "mac", tool, "computer") {
				t.Fatalf("desktop scope authorized backend tool %s", tool)
			}
		}
		if !Allows([]string{"backend:mac:computer"}, "mac", tool, "computer") {
			t.Fatal("explicit backend scope was not evaluated", tool)
		}
	}
	for _, tool := range []string{"computer.info", "computer.open", "computer.observe", "computer.act", "computer.action_status", "computer.renew", "computer.close", "computer.sessions"} {
		if Allows([]string{"computer:control:mac"}, "mac", tool, "computer") {
			t.Fatal("backend metadata bypassed its scope", tool)
		}
	}
}
