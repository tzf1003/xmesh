package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// conformance: CTR-CFG-01
func Test_CTRCFG01(t *testing.T) {
	root := "../../docs/contracts/schemas/examples"
	entries, _ := os.ReadDir(root)
	for _, ent := range entries {
		if len(ent.Name()) > 6 && ent.Name()[:6] == "xmesh." {
			t.Run(ent.Name(), func(t *testing.T) {
				if _, e := Load(filepath.Join(root, ent.Name())); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
	for _, name := range []string{"config-relay.json", "config-funnel-with-domain.json", "config-pat-empty-scopes.json", "config-lan-ephemeral.json", "config-quota-zero.json", "config-dynamic-scope-invalid.json", "config-windows-appcontainer-not-boolean.json"} {
		if _, e := Load(filepath.Join(root, "invalid", name)); e == nil {
			t.Fatal("invalid accepted", name)
		}
	}
}

func Test_CTRCFG02_ReloadState(t *testing.T) {
	b, e := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	original := s.Get()
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["node"].(map[string]any)["name"] = "changed-node"
	doc["agent"].(map[string]any)["policy"].(map[string]any)["allow_dangerous"] = true
	changed, _ := json.Marshal(doc)
	if e = os.WriteFile(path, changed, 0600); e != nil {
		t.Fatal(e)
	}
	updated := s.Get()
	if updated.Node.Name != original.Node.Name || !updated.Agent.Policy.AllowDangerous {
		t.Fatal("hot/cold generation")
	}
	if restart, failures := s.State(); !restart || failures != 0 {
		t.Fatal(restart, failures)
	}
	if e = os.WriteFile(path, []byte("invalid: ["), 0600); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if s.Get() != updated {
			t.Fatal("failed config replaced generation")
		}
	}
	if _, failures := s.State(); failures != 1 {
		t.Fatal("failure generation counted repeatedly", failures)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s.Get()
	if restart, _ := s.State(); restart {
		t.Fatal("reverted cold fields still need restart")
	}
}

// A rejected edit is reported only while the file on disk still fails to load
// (the failure count stays cumulative for sys.info).
func Test_ReloadFailureClearsWhenFileLoadsAgain(t *testing.T) {
	b, e := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	s.Get()
	if s.Failing() {
		t.Fatal("a fresh store reports a failure")
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["agent"].(map[string]any)["policy"].(map[string]any)["allow_dangerous"] = true
	valid, _ := json.Marshal(doc)
	for _, step := range []struct {
		name     string
		contents []byte
	}{{"back to the original", b}, {"a new valid edit", valid}, {"the same invalid text again", b}} {
		name, contents := step.name, step.contents
		if e = os.WriteFile(path, []byte("invalid: ["), 0600); e != nil {
			t.Fatal(e)
		}
		s.Get()
		if !s.Failing() {
			t.Fatal(name, "an invalid file is not reported")
		}
		s.Get()
		if !s.Failing() {
			t.Fatal(name, "the failure was forgotten while the file is still invalid")
		}
		if e = os.WriteFile(path, contents, 0600); e != nil {
			t.Fatal(e)
		}
		s.Get()
		if s.Failing() {
			t.Fatal(name, "the failure stayed after the file loaded again")
		}
	}
	if _, failures := s.State(); failures != 1 {
		t.Fatal("the same rejected text is counted once", failures)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	s.Get()
	if !s.Failing() {
		t.Fatal("an unreadable file is not reported")
	}
	if e = os.WriteFile(path, valid, 0600); e != nil {
		t.Fatal(e)
	}
	s.Get()
	if s.Failing() {
		t.Fatal("the failure stayed after the file was restored")
	}
}

func TestIngressPTYBudgetConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []any{nil, 3, 0, -1, 1.5, "8"} {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		ingress := doc["ingress"].(map[string]any)
		delete(ingress, "max_pty_per_client")
		if limit != nil {
			ingress["max_pty_per_client"] = limit
		}
		data, _ := json.Marshal(doc)
		cfg, err := Parse(data)
		if limit == nil || limit == 3 {
			if err != nil {
				t.Fatal(err)
			}
			want := 8
			if limit == 3 {
				want = 3
			}
			if cfg.Ingress.MaxPTYPerClient != want {
				t.Fatal("unexpected ingress limit", cfg.Ingress.MaxPTYPerClient)
			}
		} else if err == nil {
			t.Fatal("invalid ingress limit accepted", limit)
		}
	}
}

func TestIngressPTYBudgetFileConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "budget.json"), "relative/budget.json", ""} {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		doc["ingress"].(map[string]any)["pty_budget_file"] = path
		data, _ := json.Marshal(doc)
		cfg, err := Parse(data)
		if filepath.IsAbs(path) {
			if err != nil || cfg.Ingress.PTYBudgetFile != path {
				t.Fatal("absolute budget path rejected", err)
			}
		} else if err == nil {
			t.Fatal("invalid budget path accepted", path)
		}
	}
}

func TestIngressPTYBudgetAuthorityConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	for authority, valid := range map[string]bool{"": true, "home-server": true, "a": true, "bad/name": false, "-x": false, "with space": false, "under_score": false} {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		if authority != "" {
			doc["ingress"].(map[string]any)["pty_budget_authority"] = authority
		}
		data, _ := json.Marshal(doc)
		cfg, err := Parse(data)
		if valid && (err != nil || cfg.Ingress.PTYBudgetAuthority != authority) {
			t.Fatal("valid authority rejected", authority, err)
		}
		if !valid && err == nil {
			t.Fatal("invalid authority accepted", authority)
		}
	}
}

// gateway.quota and gateway.expose_backends apply in the next generation and ask for no restart.
func Test_CTRCFG01_GatewayQuotaAndExposeHot(t *testing.T) {
	b, e := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	started := s.Get().Gateway.ExposeBackends
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	gateway, _ := doc["gateway"].(map[string]any)
	if gateway == nil {
		gateway = map[string]any{}
		doc["gateway"] = gateway
	}
	next := "favorite"
	if started == "favorite" {
		next = "all"
	}
	gateway["expose_backends"] = next
	gateway["quota"] = map[string]any{"calls_per_hour": 42}
	changed, _ := json.Marshal(doc)
	if e = os.WriteFile(path, changed, 0600); e != nil {
		t.Fatal(e)
	}
	got := s.Get()
	if got.Gateway.Quota.CallsPerHour != 42 {
		t.Fatal("gateway.quota did not apply", got.Gateway.Quota)
	}
	if got.Gateway.ExposeBackends != next {
		t.Fatal("gateway.expose_backends did not apply", got.Gateway.ExposeBackends)
	}
	if pending := s.RestartPending(); len(pending) != 0 {
		t.Fatalf("a hot section asked for a restart: %v", pending)
	}
}

func ingressConfig(t *testing.T, edit func(doc map[string]any)) (*Config, error) {
	t.Helper()
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	data, _ := json.Marshal(doc)
	return Parse(data)
}

func Test_CTRCFG01_FunnelControlPlane(t *testing.T) {
	funnel := func(controlURL string) error {
		_, err := ingressConfig(t, func(doc map[string]any) {
			doc["mesh"].(map[string]any)["control_url"] = controlURL
			ingress := doc["ingress"].(map[string]any)
			for _, key := range []string{"domain", "tls"} {
				delete(ingress, key)
			}
			ingress["listen"] = "funnel"
			ingress["oauth"].(map[string]any)["issuer"] = "https://vps-mcp.tailnet.ts.net"
		})
		return err
	}
	for _, ok := range []string{"https://controlplane.tailscale.com", "https://login.tailscale.com", "https://Controlplane.Tailscale.com:443/"} {
		if err := funnel(ok); err != nil {
			t.Fatal("official control plane refused", ok, err)
		}
	}
	for _, bad := range []string{"https://hs.example.com", "http://controlplane.tailscale.com", "https://controlplane.tailscale.com.evil.example", "https://evil.example/controlplane.tailscale.com", "https://user@controlplane.tailscale.com", "https://controlplane.tailscale.com:8443", "https://tailscale.com", "https://evil-controlplane.tailscale.com"} {
		if err := funnel(bad); err == nil {
			t.Fatal("non-official control plane accepted", bad)
		}
	}
}

func Test_CTROAUTH01_IssuerOriginMatchesIngress(t *testing.T) {
	issuer := func(listen, domain, value string) error {
		_, err := ingressConfig(t, func(doc map[string]any) {
			ingress := doc["ingress"].(map[string]any)
			ingress["listen"] = listen
			if listen == "public:443" {
				ingress["domain"] = domain
			} else {
				delete(ingress, "domain")
				delete(ingress, "tls")
			}
			if listen == "funnel" {
				doc["mesh"].(map[string]any)["control_url"] = "https://controlplane.tailscale.com"
			}
			ingress["oauth"].(map[string]any)["issuer"] = value
		})
		return err
	}
	cases := []struct {
		listen, domain, issuer string
		ok                     bool
	}{
		{"public:443", "mcp.example.com", "https://mcp.example.com", true},
		{"public:443", "MCP.example.com", "https://mcp.example.com:443", true},
		{"public:443", "mcp.example.com", "https://other.example.com", false},
		{"public:443", "mcp.example.com", "https://mcp.example.com:8443", false},
		{"public:443", "mcp.example.com", "https://mcp.example.com.evil.example", false},
		{"funnel", "", "https://vps.tail1234.ts.net", true},
		{"funnel", "", "https://mcp.example.com", false},
		{"funnel", "", "https://evilts.net", false},
		{"tunnel", "", "https://mcp.example.com", true},
	}
	for _, c := range cases {
		if err := issuer(c.listen, c.domain, c.issuer); (err == nil) != c.ok {
			t.Fatal(c, err)
		}
	}
}

func Test_CONSEC02_DynamicClientScopesConfiguration(t *testing.T) {
	for name, want := range map[string][]string{"absent": nil, "empty": {}, "set": {"devices:read", "fs:read:laptop"}} {
		cfg, err := ingressConfig(t, func(doc map[string]any) {
			if name != "absent" {
				doc["ingress"].(map[string]any)["oauth"].(map[string]any)["dynamic_client_scopes"] = want
			}
		})
		if err != nil {
			t.Fatal(name, err)
		}
		got := cfg.Ingress.OAuth.DynamicClientScopes
		if (got == nil) != (want == nil) || len(got) != len(want) {
			t.Fatal(name, got)
		}
	}
	for _, scope := range []string{"root", "exec:", "fs:read:", "exec:bad name"} {
		if _, err := ingressConfig(t, func(doc map[string]any) {
			doc["ingress"].(map[string]any)["oauth"].(map[string]any)["dynamic_client_scopes"] = []string{scope}
		}); err == nil {
			t.Fatal("invalid dynamic scope accepted", scope)
		}
	}
}

func Test_CONSEC10_QuotaConfiguration(t *testing.T) {
	cfg, err := ingressConfig(t, func(map[string]any) {})
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]Quota{"agent": cfg.Agent.Quota, "gateway": cfg.Gateway.Quota} {
		hourly, concurrent, duration := q.Limits()
		if hourly != 10000 || concurrent != 64 || duration != 10*time.Minute {
			t.Fatal(name, "defaults changed", hourly, concurrent, duration)
		}
	}
	cfg, err = ingressConfig(t, func(doc map[string]any) {
		doc["agent"].(map[string]any)["quota"] = map[string]any{"calls_per_hour": 50, "max_call_duration": "45m"}
		doc["agent"].(map[string]any)["exec"] = map[string]any{"default_timeout": "30m"}
		doc["gateway"].(map[string]any)["quota"] = map[string]any{"max_concurrent": 2}
	})
	if err != nil {
		t.Fatal(err)
	}
	if h, c, d := cfg.Agent.Quota.Limits(); h != 50 || c != 64 || d != 45*time.Minute {
		t.Fatal("agent quota", h, c, d)
	}
	if h, c, d := cfg.Gateway.Quota.Limits(); h != 10000 || c != 2 || d != 10*time.Minute {
		t.Fatal("gateway quota", h, c, d)
	}
	for name, quota := range map[string]map[string]any{"zero calls": {"calls_per_hour": 0}, "negative": {"max_concurrent": -1}, "bad duration": {"max_call_duration": "soon"}, "zero duration": {"max_call_duration": "0s"}, "unknown": {"burst": 1}} {
		for _, section := range []string{"agent", "gateway"} {
			if _, err := ingressConfig(t, func(doc map[string]any) { doc[section].(map[string]any)["quota"] = quota }); err == nil {
				t.Fatal("invalid quota accepted", section, name)
			}
		}
	}
	// A default exec timeout longer than the call budget would be cut short silently.
	if _, err := ingressConfig(t, func(doc map[string]any) {
		doc["agent"].(map[string]any)["exec"] = map[string]any{"default_timeout": "30m"}
	}); err == nil {
		t.Fatal("exec default_timeout above the 10m call budget accepted")
	}
}

func Test_CONNET08_RelayMessage(t *testing.T) {
	_, e := Load(filepath.Join("../../docs/contracts/schemas/examples/invalid/config-relay.json"))
	if e == nil {
		t.Fatal("relay accepted")
	}
	if msg := e.Error(); !strings.Contains(msg, "relay") || !strings.Contains(msg, "未实现") {
		t.Fatal("error does not say relay is not implemented:", msg)
	}
	// Other unknown modes remain ordinary schema failures.
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, []byte(`{"node":{"name":"n","roles":["agent"]},"mesh":{"mode":"mesh9"},"agent":{"cwd_policy":{"allow":["/tmp"]}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(path); e == nil || strings.Contains(e.Error(), "未实现") {
		t.Fatal(e)
	}
}

func Test_CIImportConcurrency(t *testing.T) {
	for _, concurrency := range []any{nil, 1, 2, 16} {
		cfg, err := ingressConfig(t, func(doc map[string]any) {
			binding := map[string]any{"id": "legacy", "target": "o/r", "source": "import", "dir": "/runner"}
			if concurrency != nil {
				binding["concurrency"] = concurrency
			}
			doc["ci"] = map[string]any{"bindings": []any{binding}}
		})
		if concurrency == nil || concurrency == 1 {
			if err != nil || cfg.CI.Bindings[0].Concurrency != 1 {
				t.Fatalf("concurrency=%v: %v, %v", concurrency, cfg, err)
			}
			cfg.CI.Bindings[0].Concurrency = 2
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "/concurrency") {
				t.Fatalf("programmatic import concurrency accepted: %v", err)
			}
		} else if err == nil {
			t.Fatalf("import concurrency %v accepted", concurrency)
		}
	}
}
