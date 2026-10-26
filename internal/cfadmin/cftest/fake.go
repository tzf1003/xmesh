// Package cftest is an in-memory Cloudflare API for tests of code that provisions tunnels.
package cftest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
)

var normalize = regexp.MustCompile(`^/(accounts|zones)/[^/]+`)

// Fake is an in-memory Cloudflare: it records what exists and can be told to fail one route.
type Fake struct {
	mu     sync.Mutex
	Things map[string]string // kind/id -> detail
	next   int
	FailOn string
	// AccountList and ZoneList answer GET /accounts and GET /zones; left nil, the call is refused as a
	// token without the Read permission would be.
	AccountList []map[string]any
	ZoneList    []map[string]any
	Service     string // the service the last tunnel was routed to
	Requests    []string
	Auth        []string
}

// New starts a fake API for account "acct" and zone "zone". The returned URL is its base URL.
func New() (*Fake, string, func()) {
	f := &Fake{Things: map[string]string{}}
	srv := httptest.NewServer(f)
	return f, srv.URL, srv.Close
}

func (f *Fake) id(kind string) string {
	f.next++
	return kind + "-" + string(rune('a'+f.next))
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := normalize.ReplaceAllString(r.URL.Path, "/$1/$1")
	path = strings.NewReplacer("/accounts/accounts", "/accounts/acct", "/zones/zones", "/zones/zone").Replace(path)
	route := r.Method + " " + path
	f.Requests = append(f.Requests, route)
	f.Auth = append(f.Auth, r.Header.Get("Authorization"))
	reply := func(status int, result any, messages ...string) {
		errs := []map[string]any{}
		for _, m := range messages {
			errs = append(errs, map[string]any{"code": 1000, "message": m})
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": status < 300, "errors": errs, "result": result})
	}
	if f.FailOn != "" && strings.Contains(route, f.FailOn) {
		reply(500, nil, "boom")
		return
	}
	if route == "GET /accounts" || route == "GET /zones" {
		list := f.AccountList
		if route == "GET /zones" {
			list = f.ZoneList
		}
		if list == nil {
			reply(403, nil, "Authentication error")
			return
		}
		reply(200, list)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	p := strings.TrimPrefix(path, "/accounts/acct")
	switch {
	case route == "GET /accounts/acct/cfd_tunnel":
		reply(200, []any{})
	case route == "POST /accounts/acct/cfd_tunnel":
		if body["config_src"] != "cloudflare" {
			reply(400, nil, "tunnels must be remotely managed")
			return
		}
		id := f.id("tunnel")
		f.Things["tunnel/"+id] = body["name"].(string)
		reply(200, map[string]any{"id": id, "name": body["name"], "token": "tunnel-token-" + id})
	case strings.HasPrefix(p, "/cfd_tunnel/") && strings.HasSuffix(p, "/configurations") && r.Method == "PUT":
		ingress := body["config"].(map[string]any)["ingress"].([]any)
		first := ingress[0].(map[string]any)
		if !strings.HasPrefix(first["service"].(string), "tcp://127.0.0.1:") || !strings.HasSuffix(first["hostname"].(string), ".example.com") || ingress[len(ingress)-1].(map[string]any)["service"] != "http_status:404" {
			reply(400, nil, "bad ingress")
			return
		}
		f.Service = first["service"].(string)
		reply(200, map[string]any{})
	case strings.HasSuffix(p, "/connections") && r.Method == "DELETE":
		reply(200, nil)
	case strings.HasPrefix(p, "/cfd_tunnel/") && r.Method == "DELETE":
		f.drop(w, "tunnel/"+strings.TrimPrefix(p, "/cfd_tunnel/"), reply)
	case route == "POST /zones/zone/dns_records":
		if body["type"] != "CNAME" || body["proxied"] != true || !strings.HasSuffix(body["content"].(string), ".cfargotunnel.com") {
			reply(400, nil, "bad record")
			return
		}
		id := f.id("dns")
		f.Things["dns/"+id] = body["name"].(string)
		reply(200, map[string]any{"id": id})
	case strings.HasPrefix(path, "/zones/zone/dns_records/") && r.Method == "DELETE":
		f.drop(w, "dns/"+strings.TrimPrefix(path, "/zones/zone/dns_records/"), reply)
	case route == "POST /accounts/acct/access/apps":
		id := f.id("app")
		f.Things["app/"+id] = body["domain"].(string)
		reply(200, map[string]any{"id": id})
	case strings.HasPrefix(p, "/access/apps/") && strings.HasSuffix(p, "/policies"):
		if _, ok := body["include"].([]any)[0].(map[string]any)["any_valid_service_token"]; !ok || body["decision"] != "non_identity" {
			reply(400, nil, "bad policy")
			return
		}
		reply(200, map[string]any{"id": f.id("policy")})
	case strings.HasPrefix(p, "/access/apps/") && r.Method == "DELETE":
		f.drop(w, "app/"+strings.TrimPrefix(p, "/access/apps/"), reply)
	case route == "POST /accounts/acct/access/service_tokens":
		id := f.id("svc")
		f.Things["svc/"+id] = body["name"].(string)
		reply(200, map[string]any{"id": id, "client_id": "cid-" + id + ".access", "client_secret": "csecret-" + id})
	case strings.HasPrefix(p, "/access/service_tokens/") && r.Method == "GET":
		if _, ok := f.Things["svc/"+strings.TrimPrefix(p, "/access/service_tokens/")]; !ok {
			reply(404, nil, "not found")
			return
		}
		reply(200, map[string]any{})
	case strings.HasPrefix(p, "/access/service_tokens/") && r.Method == "DELETE":
		f.drop(w, "svc/"+strings.TrimPrefix(p, "/access/service_tokens/"), reply)
	default:
		reply(404, nil, "no such route")
	}
}

func (f *Fake) drop(w http.ResponseWriter, key string, reply func(int, any, ...string)) {
	if _, ok := f.Things[key]; !ok {
		reply(404, nil, "not found")
		return
	}
	delete(f.Things, key)
	reply(200, map[string]any{})
}
