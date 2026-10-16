package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func getJSON(t *testing.T, path string, h func(string) (int, http.Header, string)) map[string]any {
	t.Helper()
	status, _, body := h(path)
	if status != 200 {
		t.Fatalf("GET %s: %d %s", path, status, body)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// conformance: CTR-OAUTH-01 CON-SEC-09
func Test_CTROAUTH01_DiscoveryDocuments(t *testing.T) {
	_, h := oauthFixture(t)
	get := func(path string) (int, http.Header, string) { return request(t, h, "GET", path, "", "") }
	const issuer = "https://mesh.example"

	resource := getJSON(t, "/.well-known/oauth-protected-resource", get)
	servers, _ := resource["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != issuer {
		t.Fatalf("protected resource metadata does not point at this issuer: %v", resource)
	}
	meta := getJSON(t, "/.well-known/oauth-authorization-server", get)
	want := map[string]any{
		"issuer":                 issuer,
		"authorization_endpoint": issuer + "/oauth/authorize",
		"token_endpoint":         issuer + "/oauth/token",
		"registration_endpoint":  issuer + "/oauth/register",
	}
	for key, value := range want {
		if meta[key] != value {
			t.Fatalf("%s = %v, want %v", key, meta[key], value)
		}
	}
	lists := map[string][]string{
		"code_challenge_methods_supported": {"S256"},
		"grant_types_supported":            {"authorization_code", "refresh_token"},
		"response_types_supported":         {"code"},
	}
	for key, expected := range lists {
		raw, _ := meta[key].([]any)
		got := []string{}
		for _, v := range raw {
			got = append(got, v.(string))
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s = %v, want %v", key, got, expected)
		}
	}
	// No document ever names another origin than the configured issuer.
	for _, doc := range []map[string]any{resource, meta} {
		encoded, _ := json.Marshal(doc)
		for _, match := range regexp.MustCompile(`https?://[^"/]+`).FindAllString(string(encoded), -1) {
			if match != issuer {
				t.Fatalf("discovery document names another origin %s: %s", match, encoded)
			}
		}
	}
	// A request that arrives with another Host header never sees another issuer:
	// it is either refused outright (421) or answered with the configured issuer.
	req, _ := http.NewRequest("GET", h.URL+"/.well-known/oauth-authorization-server", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var spoofed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&spoofed)
	resp.Body.Close()
	if resp.StatusCode == http.StatusMisdirectedRequest {
		if _, leaked := spoofed["issuer"]; leaked {
			t.Fatalf("refused request still carries metadata: %v", spoofed)
		}
	} else if spoofed["issuer"] != issuer || spoofed["token_endpoint"] != issuer+"/oauth/token" {
		t.Fatalf("issuer follows the Host header: %v", spoofed)
	}
	// Discovery is read-only.
	if status, _, _ := request(t, h, "POST", "/.well-known/oauth-authorization-server", "{}", "application/json"); status != 405 {
		t.Fatalf("POST to metadata: %d", status)
	}
}

// conformance: CTR-OAUTH-01
func Test_CTROAUTH01_DynamicRegistrationSubset(t *testing.T) {
	post := func(h *httptest.Server, body string) (int, map[string]any) {
		status, _, text := request(t, h, "POST", "/oauth/register", body, "application/json")
		var doc map[string]any
		_ = json.Unmarshal([]byte(text), &doc)
		return status, doc
	}
	for name, body := range map[string]string{
		"missing client_name":      `{"redirect_uris":["https://app.example/cb"]}`,
		"missing redirect_uris":    `{"client_name":"x"}`,
		"empty redirect_uris":      `{"client_name":"x","redirect_uris":[]}`,
		"plain http redirect":      `{"client_name":"x","redirect_uris":["http://app.example/cb"]}`,
		"relative redirect":        `{"client_name":"x","redirect_uris":["/cb"]}`,
		"unsupported auth method":  `{"client_name":"x","redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_post"}`,
		"unsupported grant type":   `{"client_name":"x","redirect_uris":["https://app.example/cb"],"grant_types":["password"]}`,
		"redirect with credential": `{"client_name":"x","redirect_uris":["https://user:pw@app.example/cb"]}`,
	} {
		// Registration is rate limited per IP and failures count, so each case
		// gets a fresh server.
		_, h := oauthFixture(t)
		if status, doc := post(h, body); status != 400 {
			t.Fatalf("%s accepted: %d %v", name, status, doc)
		}
	}
	_, h := oauthFixture(t)
	// Public client: client_id only, no secret.
	status, public := post(h, `{"client_name":"cli","redirect_uris":["http://127.0.0.1:9/cb"],"token_endpoint_auth_method":"none"}`)
	if status != 201 || public["client_id"] == "" || public["client_secret"] != nil {
		t.Fatalf("public client: %d %v", status, public)
	}
	// Default grant is the authorization code.
	if grants, _ := public["grant_types"].([]any); len(grants) == 0 || grants[0] != "authorization_code" {
		t.Fatalf("default grants: %v", public["grant_types"])
	}
	// Confidential client: the secret is shown exactly once.
	status, confidential := post(h, `{"client_name":"web","redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
	secret, _ := confidential["client_secret"].(string)
	if status != 201 || secret == "" || confidential["client_id"] == "" {
		t.Fatalf("confidential client: %d %v", status, confidential)
	}
	// Registration needs no admin credential: no Authorization header was sent above.
}

// conformance: CON-SEC-09
func Test_CONSEC09_PKCEIsMandatoryAndMCPNeedsAToken(t *testing.T) {
	_, h := oauthFixture(t)
	id := registerClient(t, h)
	verifier := strings.Repeat("v", 43)
	base := func() url.Values {
		return url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"s"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(verifier)}}
	}
	authorize := func(q url.Values) int {
		status, _, _ := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", "")
		return status
	}
	if status := authorize(base()); status != 200 {
		t.Fatalf("well-formed request refused: %d", status)
	}
	mutations := map[string]func(url.Values){
		"no code_challenge":         func(q url.Values) { q.Del("code_challenge") },
		"no code_challenge_method":  func(q url.Values) { q.Del("code_challenge_method") },
		"plain method":              func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"plain challenge too short": func(q url.Values) { q.Set("code_challenge_method", "plain"); q.Set("code_challenge", "short") },
		"short S256 challenge":      func(q url.Values) { q.Set("code_challenge", "short") },
		"no state":                  func(q url.Values) { q.Del("state") },
		"token response type":       func(q url.Values) { q.Set("response_type", "token") },
		"unregistered redirect":     func(q url.Values) { q.Set("redirect_uri", "http://127.0.0.1:1234/other") },
		"unknown client":            func(q url.Values) { q.Set("client_id", "nobody") },
	}
	for name, mutate := range mutations {
		q := base()
		mutate(q)
		if status := authorize(q); status != 400 {
			t.Fatalf("%s: authorize returned %d, want 400 and no code", name, status)
		}
	}

	// The token endpoint refuses an exchange without or with a wrong verifier.
	code, _ := authCode(t, h, id, verifier)
	exchange := func(code, verifier string) int {
		form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1234/callback"}}
		if verifier != "" {
			form.Set("code_verifier", verifier)
		}
		status, _, _ := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
		return status
	}
	if status := exchange(code, ""); status != 400 {
		t.Fatalf("exchange without code_verifier: %d", status)
	}
	// A failed attempt must not leave the code usable with a guess, and a correct
	// verifier must still be required for the (now burnt or intact) code.
	if status := exchange(code, strings.Repeat("w", 43)); status != 400 {
		t.Fatalf("exchange with wrong verifier: %d", status)
	}
	if status := exchange(code, verifier); status != 400 && status != 200 {
		t.Fatalf("unexpected status %d", status)
	}

	// MCP endpoint: 401 with the resource metadata hint, for no token and a bad token.
	for name, header := range map[string]string{"no token": "", "garbage token": "Bearer not-a-token", "wrong scheme": "Basic abc"} {
		req, _ := http.NewRequest("POST", h.URL+"/mcp", strings.NewReader("{}"))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		challenge := resp.Header.Get("WWW-Authenticate")
		if resp.StatusCode != 401 || !strings.Contains(challenge, "resource_metadata=") || !strings.Contains(challenge, "/.well-known/oauth-protected-resource") {
			t.Fatalf("%s: %d %q", name, resp.StatusCode, challenge)
		}
	}
	// Every endpoint that CON-SEC-09 lists exists.
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
		if status, _, _ := request(t, h, "GET", path, "", ""); status != 200 {
			t.Fatalf("%s: %d", path, status)
		}
	}
	for _, path := range []string{"/oauth/register", "/oauth/token"} {
		if status, _, _ := request(t, h, "POST", path, "{}", "application/json"); status == 404 {
			t.Fatalf("%s missing", path)
		}
	}
}

// conformance: CTR-OAUTH-02 RUL-09
func Test_CTROAUTH02_ToolToScopeTable(t *testing.T) {
	type row struct {
		tool, backend, scope string
	}
	rows := []row{
		{"devices.list", "", "devices:read"},
		{"tools.search", "", "devices:read"},
		{"session.set_defaults", "", "devices:read"},
		{"sys.info", "", "sys:win"},
		{"sys.whoami", "", "sys:win"},
		{"exec.run", "", "exec:win"},
		{"exec.start", "", "exec:win"},
		{"pty.open", "", "pty:win"},
		{"pty.write", "", "pty:win"},
		{"fs.read", "", "fs:read:win"},
		{"fs.glob", "", "fs:read:win"},
		{"fs.grep", "", "fs:read:win"},
		{"fs.list", "", "fs:read:win"},
		{"fs.stat", "", "fs:read:win"},
		{"fs.sha256", "", "fs:read:win"},
		{"fs.write", "", "fs:write:win"},
		{"fs.edit", "", "fs:write:win"},
		{"fs.mkdir", "", "fs:write:win"},
		{"fs.move", "", "fs:write:win"},
		{"fs.delete", "", "fs:write:win"},
		{"ida.decompile_function", "", "backend:win:ida"},
		{"backend.acquire", "ida", "backend:win:ida"},
		{"backend.renew", "ida", "backend:win:ida"},
		{"backend.release", "ida", "backend:win:ida"},
	}
	// Exactly the required scope grants the tool; every other row's scope does not
	// (apart from rows that share the same required scope).
	for _, r := range rows {
		if !Allows([]string{r.scope}, "win", r.tool, r.backend) {
			t.Errorf("%s is not allowed by its own scope %q", r.tool, r.scope)
		}
		wildcard := strings.Replace(r.scope, ":win", ":*", 1)
		if r.scope == "devices:read" {
			wildcard = r.scope
		}
		if strings.HasSuffix(r.scope, ":win:ida") {
			wildcard = "backend:*:*"
		}
		if !Allows([]string{wildcard}, "win", r.tool, r.backend) {
			t.Errorf("%s is not allowed by wildcard scope %q", r.tool, wildcard)
		}
		if r.scope != "devices:read" {
			other := strings.Replace(r.scope, ":win", ":mac", 1)
			if Allows([]string{other}, "win", r.tool, r.backend) {
				t.Errorf("%s allowed on win by a scope for another device %q", r.tool, other)
			}
		}
		for _, o := range rows {
			if o.scope != r.scope && Allows([]string{o.scope}, "win", r.tool, r.backend) {
				t.Errorf("%s allowed by unrelated scope %q", r.tool, o.scope)
			}
		}
		if Allows(nil, "win", r.tool, r.backend) {
			t.Errorf("%s allowed with no scopes", r.tool)
		}
	}
	// Backend scope: wildcard backend, but not another named one.
	if !Allows([]string{"backend:win:*"}, "win", "ida.echo", "") || Allows([]string{"backend:win:ghidra"}, "win", "ida.echo", "") {
		t.Fatal("backend scope matching")
	}
	// Read-only and write-only fs tokens do not cross over (RUL-09 expanded form).
	if Allows([]string{"fs:read:*"}, "win", "fs.write", "") || Allows([]string{"fs:write:*"}, "win", "fs.read", "") {
		t.Fatal("fs read/write scopes cross over")
	}

	// RUL-09: shorthand expands to canonical scopes before comparison.
	for shorthand, canonical := range map[string][]string{
		"fs:*":       {"fs:read:*", "fs:write:*"},
		"fs:win":     {"fs:read:win", "fs:write:win"},
		"exec:*":     {"exec:*"},
		"pty:*":      {"pty:*"},
		"sys:*":      {"sys:*"},
		"fs:read:*":  {"fs:read:*"},
		"fs:write:a": {"fs:write:a"},
	} {
		got, err := Expand([]string{shorthand})
		if err != nil || !reflect.DeepEqual(got, canonical) {
			t.Fatalf("Expand(%q) = %v %v, want %v", shorthand, got, err, canonical)
		}
		// Expansion is idempotent: canonical input expands to itself.
		again, err := Expand(got)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("Expand is not idempotent for %q: %v %v", shorthand, again, err)
		}
	}
	// Duplicates collapse.
	if got, _ := Expand([]string{"fs:*", "fs:read:*"}); len(got) != 2 {
		t.Fatalf("duplicate scopes survive expansion: %v", got)
	}
	for _, bad := range []string{"", "*", "fs", "fs:read:", "fs:delete:*", "exec:read:*", "devices:write", "backend:win", "backend:*", "sys:bad/name", "fs:read:" + strings.Repeat("a", 64), "fs:read:-bad", "root"} {
		if _, err := Expand([]string{bad}); err == nil {
			t.Errorf("invalid scope %q accepted", bad)
		}
	}
	// A token whose scope list is invalid allows nothing.
	if Allows([]string{"fs:*", "bogus"}, "win", "fs.read", "") {
		t.Fatal("scope list with an invalid entry still allows tools")
	}
}

// conformance: CON-ARCH-07
func Test_CONARCH07_OAuthHasASingleLoginSubject(t *testing.T) {
	_, h := oauthFixture(t)
	id := registerClient(t, h)
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"s"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(strings.Repeat("v", 43))}}
	status, _, page := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", "")
	if status != 200 {
		t.Fatal(status)
	}
	// The only text input is the password: no username, e-mail, tenant or organization field.
	inputs := regexp.MustCompile(`<input[^>]*name="([^"]+)"`).FindAllStringSubmatch(page, -1)
	names := []string{}
	for _, m := range inputs {
		names = append(names, m[1])
	}
	if !reflect.DeepEqual(names, []string{"csrf", "password"}) {
		t.Fatalf("login form fields = %v, want only csrf and password", names)
	}
	for _, forbidden := range []string{"username", "user", "email", "tenant", "organization", "org", "workspace"} {
		if strings.Contains(strings.ToLower(page), `name="`+forbidden) {
			t.Fatalf("login form has a %s field", forbidden)
		}
	}
	// There is no user, tenant or organization management surface.
	for _, path := range []string{"/oauth/users", "/oauth/signup", "/oauth/login", "/oauth/tenants", "/oauth/organizations", "/oauth/switch", "/users", "/admin", "/oauth/userinfo"} {
		for _, method := range []string{"GET", "POST"} {
			if status, _, _ := request(t, h, method, path, "{}", "application/json"); status != 404 {
				t.Fatalf("%s %s answered %d, want 404", method, path, status)
			}
		}
	}
	// Tokens and clients carry a client identity and scopes, nothing multi-tenant.
	for _, typ := range []reflect.Type{reflect.TypeOf(Token{}), reflect.TypeOf(Client{}), reflect.TypeOf(Options{}), reflect.TypeOf(grant{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, forbidden := range []string{"tenant", "org", "user", "subject", "account", "workspace", "team"} {
				if strings.Contains(name, forbidden) {
					t.Errorf("%s.%s suggests a multi-subject model", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
	// The password is a single value in Options, not a user directory.
	if f, ok := reflect.TypeOf(Options{}).FieldByName("Password"); !ok || f.Type.Kind() != reflect.String {
		t.Fatal("Options.Password is not a single string")
	}
	// Codex and Claude are two clients of the same subject, with their own scopes.
	s, err := New(Options{Issuer: "https://mesh.example", Password: "pw", StateDir: t.TempDir(), Clients: map[string][]string{"codex": {"fs:*"}, "claude": {"devices:read"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.clients["codex"].Scopes, []string{"fs:read:*", "fs:write:*"}) || !reflect.DeepEqual(s.clients["claude"].Scopes, []string{"devices:read"}) {
		t.Fatalf("pre-registered clients: %+v %+v", s.clients["codex"], s.clients["claude"])
	}
}
