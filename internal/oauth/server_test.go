package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testScopes are what the fixtures' dynamic clients may request.
var testScopes = []string{"devices:read", "fs:read:*"}

// serve hosts the handler as the issuer's origin, which the server insists on.
func serve(s *Server, next http.Handler) *httptest.Server {
	handler := s.Handler(next)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" || strings.HasPrefix(r.Host, "127.0.0.1") {
			r.Host = s.issuerHost
		}
		handler.ServeHTTP(w, r)
	}))
}
func oauthFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, e := New(Options{Issuer: "https://mesh.example", Password: "correct horse battery staple", StateDir: t.TempDir(), DynamicScopes: testScopes})
	if e != nil {
		t.Fatal(e)
	}
	h := serve(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := FromContext(r.Context())
		writeJSON(w, 200, token.ClientID)
	}))
	t.Cleanup(h.Close)
	return s, h
}
func request(t *testing.T, h *httptest.Server, method, path, body, content string) (int, http.Header, string) {
	t.Helper()
	r, e := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", content)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}
func registerClient(t *testing.T, h *httptest.Server) string {
	t.Helper()
	status, _, body := request(t, h, "POST", "/oauth/register", `{"client_name":"codex","redirect_uris":["http://127.0.0.1:1234/callback"],"token_endpoint_auth_method":"none"}`, "application/json")
	if status != 201 {
		t.Fatal(status, body)
	}
	var v map[string]any
	_ = json.Unmarshal([]byte(body), &v)
	return v["client_id"].(string)
}
func authCode(t *testing.T, h *httptest.Server, id, verifier string, requestedScope ...string) (string, string) {
	t.Helper()
	scope := "devices:read fs:read:*"
	if len(requestedScope) > 0 {
		scope = requestedScope[0]
	}
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"test-state"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(verifier)}, "scope": {scope}}
	path := "/oauth/authorize?" + q.Encode()
	status, headers, body := request(t, h, "GET", path, "", "")
	if status != 200 {
		t.Fatal(status, body)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(csrf) != 2 {
		t.Fatal(body)
	}
	req, _ := http.NewRequest("POST", h.URL+path, strings.NewReader(url.Values{"password": {"correct horse battery staple"}, "csrf": {csrf[1]}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", strings.Split(headers.Get("Set-Cookie"), ";")[0])
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatal(resp.StatusCode, string(b))
	}
	redirect, _ := url.Parse(resp.Header.Get("Location"))
	if redirect.Query().Get("state") != "test-state" {
		t.Fatal(redirect)
	}
	return redirect.Query().Get("code"), path
}

// conformance: CTR-OAUTH-01 CON-SEC-09
func Test_CTROAUTH01_PKCEAndRotation(t *testing.T) {
	_, h := oauthFixture(t)
	status, headers, _ := request(t, h, "POST", "/mcp", `{}`, "application/json")
	if status != 401 || !strings.Contains(headers.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatal(status, headers)
	}
	id := registerClient(t, h)
	verifier := strings.Repeat("v", 43)
	code, _ := authCode(t, h, id, verifier)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code_verifier": {verifier}}
	status, _, body := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
	if status != 200 {
		t.Fatal(status, body)
	}
	var token map[string]any
	_ = json.Unmarshal([]byte(body), &token)
	status, _, _ = request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
	if status != 400 {
		t.Fatal("code replay", status)
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {token["refresh_token"].(string)}}
	status, _, body = request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded")
	if status != 200 {
		t.Fatal(status, body)
	}
	status, _, _ = request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded")
	if status != 400 {
		t.Fatal("refresh replay", status)
	}
	req, _ := http.NewRequest("POST", h.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token["access_token"].(string))
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}

// conformance: CON-SEC-11
func Test_CONSEC11_RegisterRate(t *testing.T) {
	s, h := oauthFixture(t)
	for i := 0; i < 5; i++ {
		registerClient(t, h)
	}
	status, headers, _ := request(t, h, "POST", "/oauth/register", `{}`, "application/json")
	if status != 429 || headers.Get("Retry-After") == "" {
		t.Fatal(status, headers)
	}
	s.mu.Lock()
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	s.mu.Unlock()
	registerClient(t, h)
}

// conformance: CTR-OAUTH-01 CON-SEC-09
func Test_CTROAUTH01_PKCERequiredAndRedirects(t *testing.T) {
	_, h := oauthFixture(t)
	id := registerClient(t, h)
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"test-state"}}
	status, _, _ := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", "")
	if status != 400 {
		t.Fatal(status)
	}
	for _, u := range []string{"http://example.com/cb", "javascript:alert(1)", "https://x.example/cb#fragment"} {
		if redirectValid(u) {
			t.Fatal(u)
		}
	}
}

// conformance: CTR-OAUTH-02 RUL-09
func Test_CTROAUTH02_Scopes(t *testing.T) {
	expanded, e := Expand([]string{"fs:*", "backend:win:ida"})
	if e != nil || !Allows(expanded, "win", "fs.read", "") || !Allows(expanded, "win", "fs.write", "") || !Allows(expanded, "win", "ida.echo", "") || Allows(expanded, "mac", "ida.echo", "") {
		t.Fatal(expanded, e)
	}
	if Allows([]string{"fs:write:*"}, "win", "fs.read", "") {
		t.Fatal("read allowed by write")
	}
	if _, e = Expand([]string{"fs:read:bad/name"}); e == nil {
		t.Fatal("bad scope accepted")
	}
}

func loginAttempt(t *testing.T, h *httptest.Server, path, password string, validCSRF bool) (int, http.Header) {
	t.Helper()
	status, headers, body := request(t, h, "GET", path, "", "")
	if status != 200 {
		t.Fatal(status, body)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(csrf) != 2 {
		t.Fatal("missing CSRF")
	}
	value := csrf[1]
	if !validCSRF {
		value = "invalid"
	}
	r, _ := http.NewRequest("POST", h.URL+path, strings.NewReader(url.Values{"password": {password}, "csrf": {value}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Cookie", strings.Split(headers.Get("Set-Cookie"), ";")[0])
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// conformance: CON-SEC-11
func Test_CONSEC11_LoginLockAndCSRF(t *testing.T) {
	s, h := oauthFixture(t)
	id := registerClient(t, h)
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"state"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(strings.Repeat("v", 43))}}
	path := "/oauth/authorize?" + q.Encode()
	if status, _ := loginAttempt(t, h, path, "correct horse battery staple", false); status != 400 {
		t.Fatal("CSRF", status)
	}
	for range 5 {
		if status, _ := loginAttempt(t, h, path, "incorrect", true); status != 401 {
			t.Fatal(status)
		}
	}
	if status, headers := loginAttempt(t, h, path, "correct horse battery staple", true); status != 429 || headers.Get("Retry-After") == "" {
		t.Fatal(status, headers)
	}
	s.mu.Lock()
	later := time.Now().Add(16 * time.Minute)
	s.now = func() time.Time { return later }
	s.mu.Unlock()
	if status, _ := loginAttempt(t, h, path, "correct horse battery staple", true); status != 302 {
		t.Fatal(status)
	}
}

// conformance: CON-SEC-11
func Test_CONSEC11_AuthorizeRateAndCleanup(t *testing.T) {
	s, h := oauthFixture(t)
	id := registerClient(t, h)
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"state"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(strings.Repeat("v", 43))}}
	for range 60 {
		if status, _, _ := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", ""); status != 200 {
			t.Fatal(status)
		}
	}
	if status, headers, _ := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", ""); status != 429 || headers.Get("Retry-After") == "" {
		t.Fatal(status, headers)
	}
	s.mu.Lock()
	later := time.Now().Add(6 * time.Minute)
	s.now = func() time.Time { return later }
	s.mu.Unlock()
	if status, _, _ := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", ""); status != 200 {
		t.Fatal(status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.csrf) != 1 {
		t.Fatal("expired consent state retained", len(s.csrf))
	}
}

func Test_CTROAUTH01_VerifierExpiryAndRefreshScope(t *testing.T) {
	s, h := oauthFixture(t)
	id := registerClient(t, h)
	verifier := strings.Repeat("v", 43)
	code, _ := authCode(t, h, id, verifier)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code_verifier": {strings.Repeat("x", 43)}}
	if status, _, _ := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded"); status != 400 {
		t.Fatal("bad verifier", status)
	}
	form.Set("code_verifier", verifier)
	status, _, body := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
	if status != 200 {
		t.Fatal(status, body)
	}
	var token map[string]any
	_ = json.Unmarshal([]byte(body), &token)
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {token["refresh_token"].(string)}, "scope": {"fs:write:*"}}
	if status, _, _ := request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded"); status != 400 {
		t.Fatal("scope escalation", status)
	}
	refresh.Set("scope", "fs:read:device")
	if status, _, _ := request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded"); status != 200 {
		t.Fatal("valid refresh after rejected scope", status)
	}
	code, _ = authCode(t, h, id, verifier)
	form.Set("code", code)
	s.mu.Lock()
	later := time.Now().Add(61 * time.Second)
	s.now = func() time.Time { return later }
	s.mu.Unlock()
	if status, _, _ := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded"); status != 400 {
		t.Fatal("expired code", status)
	}
}

func Test_CTROAUTH01_BasicClientPersistence(t *testing.T) {
	s, h := oauthFixture(t)
	status, _, body := request(t, h, "POST", "/oauth/register", `{"client_name":"basic","redirect_uris":["http://127.0.0.1:1234/callback"],"token_endpoint_auth_method":"client_secret_basic"}`, "application/json")
	if status != 201 {
		t.Fatal(status)
	}
	var registered map[string]any
	_ = json.Unmarshal([]byte(body), &registered)
	id, secret := registered["client_id"].(string), registered["client_secret"].(string)
	b, e := os.ReadFile(s.path)
	if e != nil || strings.Contains(string(b), secret) {
		t.Fatal("client secret persistence")
	}
	restored, e := New(Options{Issuer: s.issuer, Password: "correct horse battery staple", StateDir: filepath.Dir(s.path), DynamicScopes: testScopes})
	if e != nil {
		t.Fatal(e)
	}
	h = serve(restored, http.NotFoundHandler())
	defer h.Close()
	verifier := strings.Repeat("v", 43)
	code, _ := authCode(t, h, id, verifier)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code_verifier": {verifier}}
	if status, _, _ := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded"); status != 401 {
		t.Fatal("missing client authentication", status)
	}
	for _, password := range []string{"incorrect", secret} {
		r, _ := http.NewRequest("POST", h.URL+"/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth(id, password)
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		want := 401
		if password == secret {
			want = 200
		}
		if resp.StatusCode != want {
			t.Fatal(resp.StatusCode, want)
		}
	}
}

func newServer(t *testing.T, o Options) (*Server, *httptest.Server) {
	t.Helper()
	if o.Issuer == "" {
		o.Issuer = "https://mesh.example"
	}
	o.Password = "correct horse battery staple"
	if o.StateDir == "" {
		o.StateDir = t.TempDir()
	}
	s, e := New(o)
	if e != nil {
		t.Fatal(e)
	}
	h := serve(s, http.NotFoundHandler())
	t.Cleanup(h.Close)
	return s, h
}
func registeredScopes(t *testing.T, h *httptest.Server) (string, []string) {
	t.Helper()
	status, _, body := request(t, h, "POST", "/oauth/register", `{"client_name":"codex","redirect_uris":["http://127.0.0.1:1234/callback"],"token_endpoint_auth_method":"none"}`, "application/json")
	if status != 201 {
		t.Fatal(status, body)
	}
	var v struct {
		ID    string `json:"client_id"`
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal([]byte(body), &v)
	return v.ID, strings.Fields(v.Scope)
}
func authorizeStatus(t *testing.T, h *httptest.Server, id, scope string) int {
	t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"s"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(strings.Repeat("v", 43))}, "scope": {scope}}
	status, _, _ := request(t, h, "GET", "/oauth/authorize?"+q.Encode(), "", "")
	return status
}

func Test_CONSEC02_DynamicClientScopeCap(t *testing.T) {
	_, h := newServer(t, Options{})
	id, scopes := registeredScopes(t, h)
	if len(scopes) != 1 || scopes[0] != "devices:read" {
		t.Fatal("default cap", scopes)
	}
	if got := authorizeStatus(t, h, id, "devices:read"); got != 200 {
		t.Fatal("devices:read refused", got)
	}
	for _, scope := range []string{"exec:*", "fs:write:*", "backend:*:*", "devices:read exec:laptop"} {
		if got := authorizeStatus(t, h, id, scope); got != 400 {
			t.Fatal("scope above cap accepted", scope, got)
		}
	}
	// A client that asks for nothing gets the cap, not every scope.
	if got := authorizeStatus(t, h, id, "devices:read"); got != 200 {
		t.Fatal(got)
	}
}
func Test_CONSEC02_DynamicClientScopesConfigured(t *testing.T) {
	_, h := newServer(t, Options{DynamicScopes: []string{"devices:read", "fs:read:laptop"}})
	id, scopes := registeredScopes(t, h)
	if strings.Join(scopes, " ") != "devices:read fs:read:laptop" {
		t.Fatal(scopes)
	}
	if authorizeStatus(t, h, id, "fs:read:laptop") != 200 || authorizeStatus(t, h, id, "fs:read:other") != 400 {
		t.Fatal("configured cap")
	}
	_, h = newServer(t, Options{DynamicScopes: []string{}})
	if _, scopes = registeredScopes(t, h); len(scopes) != 0 {
		t.Fatal("empty cap", scopes)
	}
	if _, e := New(Options{Issuer: "https://mesh.example", Password: "x", DynamicScopes: []string{"root"}}); e == nil {
		t.Fatal("invalid dynamic scope accepted")
	}
}
func Test_CONSEC02_PersistedDynamicClientsAreClamped(t *testing.T) {
	dir := t.TempDir()
	old := map[string]Client{
		"legacy":     {ID: "legacy", Name: "legacy", Method: "none", Redirects: []string{"http://127.0.0.1:1/cb"}, Scopes: []string{"devices:read", "exec:*", "fs:write:*"}},
		"configured": {ID: "configured", Name: "configured", Method: "none", Scopes: []string{"exec:*"}},
	}
	b, _ := json.Marshal(old)
	if e := os.WriteFile(filepath.Join(dir, "oauth-clients.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New(Options{Issuer: "https://mesh.example", Password: "x", StateDir: dir, Clients: map[string][]string{"configured": {"exec:*"}}})
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(s.clients["legacy"].Scopes, " "); got != "devices:read" {
		t.Fatal("legacy dynamic client keeps", got)
	}
	if got := strings.Join(s.clients["configured"].Scopes, " "); got != "exec:*" {
		t.Fatal("configured client changed", got)
	}
}
func forwarded(t *testing.T, h *httptest.Server, path, forwardedFor string) int {
	t.Helper()
	r, _ := http.NewRequest("POST", h.URL+path, strings.NewReader(`{"client_name":"c","redirect_uris":["http://127.0.0.1:1234/callback"]}`))
	r.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	return resp.StatusCode
}
func Test_CONSEC11_ForwardedForOnlyInTunnel(t *testing.T) {
	// Without a trusted proxy the header is attacker-controlled: rotating it
	// must not reset the register bucket.
	_, direct := newServer(t, Options{})
	for i := 0; i < 5; i++ {
		if got := forwarded(t, direct, "/oauth/register", fmt.Sprintf("203.0.113.%d", i)); got != 201 {
			t.Fatal(i, got)
		}
	}
	if got := forwarded(t, direct, "/oauth/register", "198.51.100.9"); got != 429 {
		t.Fatal("forged X-Forwarded-For escaped the limit", got)
	}
	// Behind the tunnel proxy the last entry is the real client; earlier ones are forged.
	_, tunnel := newServer(t, Options{Proxied: true})
	for i := 0; i < 5; i++ {
		if got := forwarded(t, tunnel, "/oauth/register", fmt.Sprintf("10.9.9.%d, 203.0.113.7", i)); got != 201 {
			t.Fatal(i, got)
		}
	}
	if got := forwarded(t, tunnel, "/oauth/register", "10.8.8.8, 203.0.113.7"); got != 429 {
		t.Fatal("same proxied client was not limited", got)
	}
	if got := forwarded(t, tunnel, "/oauth/register", "203.0.113.8"); got != 201 {
		t.Fatal("different proxied client shares the bucket", got)
	}
	// A proxy that sent nothing usable falls back to the socket peer.
	if got := forwarded(t, tunnel, "/oauth/register", "not-an-ip"); got != 201 {
		t.Fatal(got)
	}
}
func Test_CONSEC11_ForwardedForKeysLoginLock(t *testing.T) {
	s, _ := newServer(t, Options{Proxied: true})
	r := httptest.NewRequest("GET", "/oauth/authorize", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "2001:db8::1, ::ffff:192.0.2.5")
	if got := s.clientIP(r); got != "192.0.2.5" {
		t.Fatal(got)
	}
	r.Header.Del("X-Forwarded-For")
	if got := s.clientIP(r); got != "127.0.0.1" {
		t.Fatal(got)
	}
}
func Test_CTROAUTH01_IssuerOrigin(t *testing.T) {
	_, h := newServer(t, Options{})
	get := func(host, path string) int {
		r, _ := http.NewRequest("GET", h.URL+path, nil)
		r.Host = host
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// serve() rewrites loopback hosts, so use names it leaves alone.
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource", "/oauth/authorize", "/mcp"} {
		if got := get("evil.example", path); got != 421 {
			t.Fatal(path, "answered for a foreign host", got)
		}
	}
	for _, host := range []string{"mesh.example", "MESH.example", "mesh.example:443"} {
		if got := get(host, "/.well-known/oauth-authorization-server"); got != 200 {
			t.Fatal(host, got)
		}
	}
	// A forwarded host is ignored unless a trusted proxy fronts the listener.
	r, _ := http.NewRequest("GET", h.URL+"/.well-known/oauth-authorization-server", nil)
	r.Host = "evil.example"
	r.Header.Set("X-Forwarded-Host", "mesh.example")
	resp, _ := http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != 421 {
		t.Fatal("X-Forwarded-Host trusted without a proxy", resp.StatusCode)
	}
	_, proxied := newServer(t, Options{Proxied: true})
	r, _ = http.NewRequest("GET", proxied.URL+"/.well-known/oauth-authorization-server", nil)
	r.Host = "origin.internal:8443"
	r.Header.Set("X-Forwarded-Host", "mesh.example")
	resp, _ = http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("proxied origin refused", resp.StatusCode)
	}
	// The listener behind the proxy is loopback-only, so a proxy that rewrites
	// Host to it is accepted, unless it also names a foreign origin.
	s, _ := newServer(t, Options{Proxied: true})
	direct := func(host, forwarded string) int {
		r := httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
		r.Host = host
		if forwarded != "" {
			r.Header.Set("X-Forwarded-Host", forwarded)
		}
		w := httptest.NewRecorder()
		s.Handler(http.NotFoundHandler()).ServeHTTP(w, r)
		return w.Code
	}
	for host, want := range map[string]int{"127.0.0.1:8443": 200, "[::1]:8443": 200, "localhost:8443": 200, "mesh.example": 200, "evil.example": 421, "127.0.0.1.evil.example": 421} {
		if got := direct(host, ""); got != want {
			t.Fatal(host, got)
		}
	}
	if got := direct("127.0.0.1:8443", "evil.example"); got != 421 {
		t.Fatal("foreign X-Forwarded-Host accepted", got)
	}
	if got := direct("evil.example", "mesh.example, evil.example"); got != 421 {
		t.Fatal("forwarded host is the last entry", got)
	}
	// Without a proxy a loopback Host is just another foreign host.
	plain, _ := newServer(t, Options{})
	r = httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	r.Host = "127.0.0.1:8443"
	w := httptest.NewRecorder()
	plain.Handler(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal("loopback Host accepted without a proxy", w.Code)
	}
}

// A revoked client loses its registration and every token; a rotated password applies at once.
func Test_ControlRevokeClientAndSetPassword(t *testing.T) {
	dir := t.TempDir()
	s, e := New(Options{Issuer: "https://mesh.example", Password: "correct horse battery staple", StateDir: dir, DynamicScopes: testScopes})
	if e != nil {
		t.Fatal(e)
	}
	h := serve(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, "ok") }))
	t.Cleanup(h.Close)
	id := registerClient(t, h)
	verifier := strings.Repeat("v", 43)
	code, _ := authCode(t, h, id, verifier)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code_verifier": {verifier}}
	status, _, body := request(t, h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
	if status != 200 {
		t.Fatal(status, body)
	}
	var token map[string]any
	_ = json.Unmarshal([]byte(body), &token)
	call := func() int {
		req, _ := http.NewRequest("POST", h.URL+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token["access_token"].(string))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if call() != 200 {
		t.Fatal("token should work before revocation")
	}
	clients := s.Clients()
	if len(clients) != 1 || clients[0].ID != id || clients[0].SecretHash != "" {
		t.Fatalf("%+v", clients)
	}
	if found, err := s.RevokeClient("missing"); found || err != nil {
		t.Fatal(found, err)
	}
	if found, err := s.RevokeClient(id); !found || err != nil {
		t.Fatal(found, err)
	}
	if call() != 401 {
		t.Fatal("access token survived revocation")
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {token["refresh_token"].(string)}}
	if status, _, _ = request(t, h, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded"); status != 401 {
		t.Fatal("refresh token survived revocation", status)
	}
	if len(s.Clients()) != 0 {
		t.Fatal("client still listed")
	}
	reopened, e := New(Options{Issuer: "https://mesh.example", Password: "x", StateDir: dir})
	if e != nil || len(reopened.Clients()) != 0 {
		t.Fatal("revocation was not persisted", e)
	}

	other := registerClient(t, h)
	q := url.Values{"response_type": {"code"}, "client_id": {other}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"s"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(verifier)}}
	path := "/oauth/authorize?" + q.Encode()
	s.SetPassword("a brand new password")
	if status, _ := loginAttempt(t, h, path, "correct horse battery staple", true); status != 401 {
		t.Fatal("old password still works", status)
	}
	if status, _ := loginAttempt(t, h, path, "a brand new password", true); status != 302 {
		t.Fatal("new password refused", status)
	}
}
