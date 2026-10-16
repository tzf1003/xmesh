package oauth

import (
	"encoding/json"
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

func oauthFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, e := New(Options{Issuer: "https://mesh.example", Password: "correct horse battery staple", StateDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	h := httptest.NewServer(s.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := FromContext(r.Context())
		writeJSON(w, 200, token.ClientID)
	})))
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
func authCode(t *testing.T, h *httptest.Server, id, verifier string) (string, string) {
	t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"test-state"}, "code_challenge_method": {"S256"}, "code_challenge": {digest(verifier)}, "scope": {"devices:read fs:read:*"}}
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
	restored, e := New(Options{Issuer: s.issuer, Password: "correct horse battery staple", StateDir: filepath.Dir(s.path)})
	if e != nil {
		t.Fatal(e)
	}
	h = httptest.NewServer(restored.Handler(http.NotFoundHandler()))
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
