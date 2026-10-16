package meshadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTailscale is a minimal Tailscale API v2 server: OAuth client credentials,
// devices, tags, auth keys and the policy file with ETag/If-Match.
type fakeTailscale struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	devices []map[string]any
	policy  string
	scopes  map[string][]string // access token -> scopes
	lastKey map[string]any
	// seen records "METHOD path" for assertions
	seen []string
}

const fakeSecret = "tskey-client-SECRET-DO-NOT-LEAK"

func newFakeTailscale(t *testing.T) *fakeTailscale {
	f := &fakeTailscale{t: t, scopes: map[string][]string{}, policy: "{\n\t// base\n\t\"acls\": [],\n}\n"}
	f.devices = []map[string]any{
		{"id": "1001", "name": "laptop.tail.ts.net", "hostname": "laptop", "addresses": []string{"100.64.0.1"}, "tags": []string{"tag:device"}, "authorized": true, "connectedToControl": true, "nodeKey": "nodekey:" + strings.Repeat("a1", 32), "keyExpiryDisabled": true, "lastSeen": "2026-10-16T10:00:00Z", "expires": "0001-01-01T00:00:00Z", "os": "linux", "clientVersion": "1.80.0"},
		{"id": "1002", "name": "phone.tail.ts.net", "hostname": "phone", "addresses": []string{"100.64.0.2"}, "tags": []string{}, "authorized": true, "connectedToControl": false, "nodeKey": "nodekey:" + strings.Repeat("b2", 32), "expires": "2026-10-18T00:00:00Z", "user": "alice@example.com"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth/token", f.token)
	mux.HandleFunc("/api/v2/", f.api)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTailscale) etag() string {
	sum := sha256.Sum256([]byte(f.policy))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func (f *fakeTailscale) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, _ := r.BasicAuth()
	if id == "" {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if id != "cid" || secret != fakeSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	tok := "tok-" + strings.ReplaceAll(r.Form.Get("scope"), " ", "_")
	f.mu.Lock()
	f.scopes[tok] = strings.Fields(strings.ReplaceAll(r.Form.Get("scope"), ",", " "))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
}

func (f *fakeTailscale) has(r *http.Request, scope string) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "api-token" {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.scopes[tok] {
		if s == scope {
			return true
		}
	}
	return false
}

func deny(w http.ResponseWriter, scope string) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, `{"message":"requires scope `+scope+`"}`)
}

func (f *fakeTailscale) api(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case p == "tailnet/-/devices" && r.Method == http.MethodGet:
		if !f.has(r, "devices:core:read") && !f.has(r, "devices:core") {
			deny(w, "devices:core:read")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": f.devices})
	case strings.HasPrefix(p, "device/") && strings.HasSuffix(p, "/tags") && r.Method == http.MethodPost:
		if !f.has(r, "devices:core") {
			deny(w, "devices:core")
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(p, "device/"), "/tags")
		var body struct{ Tags []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, d := range f.devices {
			if d["id"] == id {
				d["tags"] = body.Tags
				_, _ = io.WriteString(w, "{}")
				return
			}
		}
		http.Error(w, `{"message":"device not found"}`, http.StatusNotFound)
	case strings.HasPrefix(p, "device/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(p, "device/")
		for _, d := range f.devices {
			if d["id"] == id {
				_ = json.NewEncoder(w).Encode(d)
				return
			}
		}
		http.Error(w, `{"message":"device not found"}`, http.StatusNotFound)
	case p == "tailnet/-/keys" && r.Method == http.MethodPost:
		if !f.has(r, "auth_keys") {
			deny(w, "auth_keys")
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.lastKey = body
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "k1", "key": "tskey-auth-NEWKEY", "expires": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	case p == "tailnet/-/acl" && r.Method == http.MethodGet:
		if !f.has(r, "policy_file:read") && !f.has(r, "policy_file") {
			deny(w, "policy_file:read")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("ETag", f.etag())
		w.Header().Set("Content-Type", "application/hujson")
		_, _ = io.WriteString(w, f.policy)
	case p == "tailnet/-/acl" && r.Method == http.MethodPost:
		if !f.has(r, "policy_file") {
			deny(w, "policy_file")
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if m := r.Header.Get("If-Match"); m != "" && m != f.etag() {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `{"message":"precondition failed"}`)
			return
		}
		f.policy = string(body)
		w.Header().Set("ETag", f.etag())
		_, _ = io.WriteString(w, "{}")
	case p == "tailnet/-/acl/validate" && r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "BADRULE") {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "test(s) failed", "data": []map[string]any{{"user": "x", "errors": []string{"BADRULE is not allowed"}}}})
			return
		}
		_, _ = io.WriteString(w, "{}")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTailscale) setPolicy(s string) {
	f.mu.Lock()
	f.policy = s
	f.mu.Unlock()
}
