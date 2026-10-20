package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
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

func TestComputerScopeCapsPermitObservationWithoutGrantingControl(t *testing.T) {
	for _, cap := range []string{"computer:control:mac", "computer:control:*"} {
		t.Run(cap, func(t *testing.T) {
			s, err := New(Options{Issuer: "https://mesh.example", Password: "correct horse battery staple", DynamicScopes: []string{cap}, Clients: map[string][]string{"configured": {cap}}})
			if err != nil {
				t.Fatal(err)
			}
			h := serve(s, http.NotFoundHandler())
			defer h.Close()
			for _, id := range []string{"configured", registerClient(t, h)} {
				verifier := strings.Repeat("v", 43)
				code, _ := authCode(t, h, id, verifier, "computer:observe:mac")
				form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code_verifier": {verifier}}
				status, _, body := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
				if status != http.StatusOK {
					t.Fatal(status, body)
				}
				// expires_in is numeric; decode only the fields needed here.
				var response struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					Scope        string `json:"scope"`
				}
				if err := json.Unmarshal([]byte(body), &response); err != nil || response.Scope != "computer:observe:mac" {
					t.Fatal(response, err)
				}
				s.mu.Lock()
				granted := s.access[digest(response.AccessToken)].Scopes
				s.mu.Unlock()
				if !Allows(granted, "mac", "computer.observe", "") || Allows(granted, "mac", "computer.act", "") {
					t.Fatal("observation request acquired control", granted)
				}
				refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {response.RefreshToken}, "scope": {"computer:control:mac"}}
				status, _, _ = request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded")
				if status != http.StatusBadRequest {
					t.Fatal("refresh escalated observation", status)
				}
				refresh.Set("scope", "computer:observe:mac")
				status, _, body = request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded")
				if status != http.StatusOK {
					t.Fatal(status, body)
				}
			}
		})
	}
	for _, denied := range [][2]string{
		{"computer:control:mac", "computer:observe:mac"},
		{"computer:observe:win", "computer:control:mac"},
		{"computer:observe:*", "computer:control:mac"},
		{"computer:clipboard-read:mac", "computer:control:mac"},
		{"computer:clipboard-write:mac", "computer:control:*"},
	} {
		if subset([]string{denied[0]}, []string{denied[1]}) {
			t.Fatal("scope cap broadened", denied)
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
