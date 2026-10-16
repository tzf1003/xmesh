package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var errScope = errors.New("scope 无效")

type Client struct {
	ID         string   `json:"client_id"`
	Name       string   `json:"client_name"`
	Redirects  []string `json:"redirect_uris"`
	Method     string   `json:"token_endpoint_auth_method"`
	SecretHash string   `json:"secret_hash,omitempty"`
	Scopes     []string `json:"scopes"`
}
type grant struct {
	Client, Redirect, Challenge, Resource string
	Scopes                                []string
	Expires                               time.Time
}
type Token struct {
	ClientID string
	Scopes   []string
	Expires  time.Time
	Family   string
}
type loginLimit struct {
	Failures    int
	LockedUntil time.Time
}
type Server struct {
	mu              sync.Mutex
	issuer          string
	passwordHash    [32]byte
	clients         map[string]Client
	codes           map[string]grant
	access, refresh map[string]Token
	csrf            map[string]time.Time
	windows         map[string][]time.Time
	login           map[string]loginLimit
	path            string
	now             func() time.Time
	maxScopes       []string
}
type Options struct {
	Issuer, Password, StateDir string
	Clients                    map[string][]string
}

func New(o Options) (*Server, error) {
	u, e := url.Parse(o.Issuer)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("OAuth issuer 必须是 HTTPS 源")
	}
	if o.Password == "" {
		return nil, fmt.Errorf("入口需要单主体登录口令文件")
	}
	s := &Server{issuer: o.Issuer, passwordHash: sha256.Sum256([]byte(o.Password)), clients: map[string]Client{}, codes: map[string]grant{}, access: map[string]Token{}, refresh: map[string]Token{}, csrf: map[string]time.Time{}, windows: map[string][]time.Time{}, login: map[string]loginLimit{}, now: time.Now, maxScopes: []string{"devices:read", "exec:*", "pty:*", "fs:read:*", "fs:write:*", "sys:*", "backend:*:*"}}
	if o.StateDir != "" {
		if e = os.MkdirAll(o.StateDir, 0700); e != nil {
			return nil, e
		}
		s.path = filepath.Join(o.StateDir, "oauth-clients.json")
		b, e := os.ReadFile(s.path)
		if e == nil {
			if e = json.Unmarshal(b, &s.clients); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	for id, scopes := range o.Clients {
		expanded, e := Expand(scopes)
		if e != nil {
			return nil, e
		}
		if existing, ok := s.clients[id]; ok {
			existing.Scopes = expanded
			s.clients[id] = existing
		} else {
			s.clients[id] = Client{ID: id, Name: id, Method: "none", Scopes: expanded}
		}
	}
	return s, nil
}
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(text string) string {
	h := sha256.Sum256([]byte(text))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func (s *Server) limited(w http.ResponseWriter, r *http.Request, action string, n int, window time.Duration) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := action + ":" + host
	now := s.now()
	old := s.windows[key]
	kept := old[:0]
	for _, v := range old {
		if v.After(now.Add(-window)) {
			kept = append(kept, v)
		}
	}
	s.windows[key] = kept
	if len(kept) >= n {
		seconds := max(1, int(kept[0].Add(window).Sub(now).Seconds())+1)
		w.Header().Set("Retry-After", fmt.Sprint(seconds))
		oauthError(w, 429, "rate_limited")
		return true
	}
	s.windows[key] = append(kept, now)
	return false
}
func (s *Server) Handler(mcp http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
			if r.Method != "GET" {
				oauthError(w, 405, "invalid_request")
				return
			}
			writeJSON(w, 200, map[string]any{"resource": s.issuer + "/mcp", "authorization_servers": []string{s.issuer}, "bearer_methods_supported": []string{"header"}, "scopes_supported": s.maxScopes})
		case "/.well-known/oauth-authorization-server":
			if r.Method != "GET" {
				oauthError(w, 405, "invalid_request")
				return
			}
			writeJSON(w, 200, map[string]any{"issuer": s.issuer, "authorization_endpoint": s.issuer + "/oauth/authorize", "token_endpoint": s.issuer + "/oauth/token", "registration_endpoint": s.issuer + "/oauth/register", "code_challenge_methods_supported": []string{"S256"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "response_types_supported": []string{"code"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic"}, "scopes_supported": s.maxScopes})
		case "/oauth/register":
			s.register(w, r)
		case "/oauth/authorize":
			s.authorize(w, r)
		case "/oauth/token":
			s.token(w, r)
		case "/mcp":
			token, ok := s.Authenticate(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.issuer+`/.well-known/oauth-protected-resource"`)
				oauthError(w, 401, "invalid_token")
				return
			}
			mcp.ServeHTTP(w, r.WithContext(withToken(r.Context(), token)))
		default:
			http.NotFound(w, r)
		}
	})
}
func redirectValid(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.IsAbs() && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (u.Scheme == "http" && u.Hostname() == "127.0.0.1"))
}
func (s *Server) saveClients() error {
	if s.path == "" {
		return nil
	}
	b, e := json.Marshal(s.clients)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".oauth-clients-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), s.path)
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		oauthError(w, 405, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if s.limited(w, r, "register", 5, time.Hour) {
		return
	}
	var req struct {
		Name      string   `json:"client_name"`
		Redirects []string `json:"redirect_uris"`
		Grants    []string `json:"grant_types"`
		Method    string   `json:"token_endpoint_auth_method"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.Name == "" || len(req.Name) > 200 || len(req.Redirects) == 0 || len(req.Redirects) > 20 {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	for _, u := range req.Redirects {
		if !redirectValid(u) {
			oauthError(w, 400, "invalid_redirect_uri")
			return
		}
	}
	for _, g := range req.Grants {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, 400, "invalid_client_metadata")
			return
		}
	}
	if req.Method == "" {
		req.Method = "none"
	}
	if req.Method != "none" && req.Method != "client_secret_basic" {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	c := Client{ID: random(), Name: req.Name, Redirects: req.Redirects, Method: req.Method, Scopes: append([]string{}, s.maxScopes...)}
	secret := ""
	if c.Method == "client_secret_basic" {
		secret = random()
		c.SecretHash = digest(secret)
	}
	s.clients[c.ID] = c
	if e := s.saveClients(); e != nil {
		delete(s.clients, c.ID)
		oauthError(w, 500, "server_error")
		return
	}
	response := map[string]any{"client_id": c.ID, "client_name": c.Name, "redirect_uris": c.Redirects, "token_endpoint_auth_method": c.Method, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}}
	if secret != "" {
		response["client_secret"] = secret
	}
	writeJSON(w, 201, response)
}

var challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>XmeSh 授权</title><body><main><h1>允许连接 XmeSh</h1><p>客户端：{{.Name}}</p><p>权限：{{.Scopes}}</p><form method="post" action="{{.Action}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>登录口令 <input type="password" name="password" autocomplete="current-password" required></label><button type="submit">登录并授权</button></form></main></body></html>`))

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		oauthError(w, 405, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if r.Method == "GET" && s.limited(w, r, "authorize", 60, time.Minute) {
		return
	}
	q := r.URL.Query()
	c, ok := s.clients[q.Get("client_id")]
	if !ok || q.Get("response_type") != "code" || q.Get("state") == "" || q.Get("code_challenge_method") != "S256" || !challengePattern.MatchString(q.Get("code_challenge")) {
		oauthError(w, 400, "invalid_request")
		return
	}
	redirect := q.Get("redirect_uri")
	if !redirectValid(redirect) {
		oauthError(w, 400, "invalid_redirect_uri")
		return
	}
	registered := false
	for _, uri := range c.Redirects {
		if uri == redirect {
			registered = true
		}
	}
	if !registered && len(c.Redirects) > 0 {
		oauthError(w, 400, "invalid_redirect_uri")
		return
	}
	resource := q.Get("resource")
	if resource != "" && resource != s.issuer+"/mcp" {
		oauthError(w, 400, "invalid_target")
		return
	}
	scopes := c.Scopes
	if q.Has("scope") {
		var e error
		scopes, e = Expand(strings.Fields(q.Get("scope")))
		if e != nil || !subset(scopes, c.Scopes) {
			oauthError(w, 400, "invalid_scope")
			return
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if r.Method == "GET" {
		csrf := random()
		s.csrf[digest(csrf+"|"+r.URL.RawQuery)] = s.now().Add(5 * time.Minute)
		http.SetCookie(w, &http.Cookie{Name: "xmesh_csrf", Value: csrf, Path: "/oauth/authorize", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 300})
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginPage.Execute(w, map[string]string{"Name": c.Name, "Scopes": strings.Join(scopes, " "), "Action": "/oauth/authorize?" + r.URL.RawQuery, "CSRF": csrf})
		return
	}
	limit := s.login[host]
	if limit.LockedUntil.After(s.now()) {
		w.Header().Set("Retry-After", fmt.Sprint(max(1, int(limit.LockedUntil.Sub(s.now()).Seconds()))))
		oauthError(w, 429, "rate_limited")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	cookie, e := r.Cookie("xmesh_csrf")
	csrf := r.PostForm.Get("csrf")
	csrfKey := digest(csrf + "|" + r.URL.RawQuery)
	expires, exists := s.csrf[csrfKey]
	if e != nil || cookie.Value != csrf || !exists || !expires.After(s.now()) {
		oauthError(w, 400, "invalid_request")
		return
	}
	password := sha256.Sum256([]byte(r.PostForm.Get("password")))
	if subtle.ConstantTimeCompare(password[:], s.passwordHash[:]) != 1 {
		limit.Failures++
		if limit.Failures >= 5 {
			limit.LockedUntil = s.now().Add(15 * time.Minute)
			limit.Failures = 0
		}
		s.login[host] = limit
		oauthError(w, 401, "access_denied")
		return
	}
	delete(s.login, host)
	delete(s.csrf, csrfKey)
	// Configuration provides scopes before a CLI has chosen its loopback port.
	// Bind the first redirect only after the subject's authenticated consent.
	if len(c.Redirects) == 0 {
		previous := c
		c.Redirects = []string{redirect}
		s.clients[c.ID] = c
		if e := s.saveClients(); e != nil {
			s.clients[c.ID] = previous
			oauthError(w, 500, "server_error")
			return
		}
	}
	code := random()
	s.codes[digest(code)] = grant{Client: c.ID, Redirect: redirect, Challenge: q.Get("code_challenge"), Resource: resource, Scopes: scopes, Expires: s.now().Add(time.Minute)}
	target, _ := url.Parse(redirect)
	values := target.Query()
	values.Set("code", code)
	values.Set("state", q.Get("state"))
	target.RawQuery = values.Encode()
	http.Redirect(w, r, target.String(), 302)
}
func subset(requested, allowed []string) bool {
	for _, scope := range requested {
		found := false
		for _, a := range allowed {
			match := a == scope
			need, parts := strings.Split(scope, ":"), strings.Split(a, ":")
			if len(need) == len(parts) {
				match = true
				for i, p := range parts {
					if p != "*" && p != need[i] {
						match = false
						break
					}
				}
			}
			if match {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		oauthError(w, 405, "invalid_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	id := r.PostForm.Get("client_id")
	basicID, secret, basic := r.BasicAuth()
	if basic {
		if id != "" && id != basicID {
			oauthError(w, 401, "invalid_client")
			return
		}
		id = basicID
	}
	c, ok := s.clients[id]
	if !ok || (c.Method == "client_secret_basic" && (!basic || subtle.ConstantTimeCompare([]byte(digest(secret)), []byte(c.SecretHash)) != 1)) || (c.Method == "none" && basic) {
		oauthError(w, 401, "invalid_client")
		return
	}
	var scopes []string
	family := ""
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		key := digest(r.PostForm.Get("code"))
		g, exists := s.codes[key]
		if !exists || g.Client != id || !g.Expires.After(s.now()) || g.Redirect != r.PostForm.Get("redirect_uri") || !verifierPattern.MatchString(r.PostForm.Get("code_verifier")) || g.Challenge != digest(r.PostForm.Get("code_verifier")) || (g.Resource != "" && g.Resource != r.PostForm.Get("resource")) {
			oauthError(w, 400, "invalid_grant")
			return
		}
		delete(s.codes, key)
		scopes = g.Scopes
		family = random()
	case "refresh_token":
		key := digest(r.PostForm.Get("refresh_token"))
		g, exists := s.refresh[key]
		if !exists || g.ClientID != id || !g.Expires.After(s.now()) {
			oauthError(w, 400, "invalid_grant")
			return
		}
		scopes = g.Scopes
		family = g.Family
		if r.PostForm.Has("scope") {
			var e error
			scopes, e = Expand(strings.Fields(r.PostForm.Get("scope")))
			if e != nil || !subset(scopes, g.Scopes) {
				oauthError(w, 400, "invalid_scope")
				return
			}
		}
		delete(s.refresh, key)
	default:
		oauthError(w, 400, "unsupported_grant_type")
		return
	}
	access, refresh := random(), random()
	s.access[digest(access)] = Token{id, scopes, s.now().Add(time.Hour), family}
	s.refresh[digest(refresh)] = Token{id, scopes, s.now().Add(30 * 24 * time.Hour), family}
	s.sweep()
	writeJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh, "scope": strings.Join(scopes, " ")})
}
func (s *Server) Authenticate(r *http.Request) (Token, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return Token{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.access[digest(token)]
	if !ok || !t.Expires.After(s.now()) {
		return Token{}, false
	}
	return t, true
}
func (s *Server) sweep() {
	now := s.now()
	for k, v := range s.codes {
		if !v.Expires.After(now) {
			delete(s.codes, k)
		}
	}
	for k, v := range s.access {
		if !v.Expires.After(now) {
			delete(s.access, k)
		}
	}
	for k, v := range s.refresh {
		if !v.Expires.After(now) {
			delete(s.refresh, k)
		}
	}
	for k, v := range s.csrf {
		if !v.After(now) {
			delete(s.csrf, k)
		}
	}
	for key, values := range s.windows {
		window := time.Minute
		if strings.HasPrefix(key, "register:") {
			window = time.Hour
		}
		kept := values[:0]
		for _, value := range values {
			if now.Sub(value) < window {
				kept = append(kept, value)
			}
		}
		if len(kept) == 0 {
			delete(s.windows, key)
		} else {
			s.windows[key] = kept
		}
	}
	for key, limit := range s.login {
		if !limit.LockedUntil.IsZero() && !limit.LockedUntil.After(now) {
			delete(s.login, key)
		}
	}
}
