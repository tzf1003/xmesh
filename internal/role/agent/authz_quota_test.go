package agent

import (
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"
)

func Test_CTRAUTHZ01_WildcardTags(t *testing.T) {
	grant := func(tools ...string) config.Grant { return config.Grant{Backends: []string{"ida"}, Tools: tools} }
	cases := []struct {
		name    string
		authz   map[string]config.Grant
		tags    []string
		tool    string
		backend string
		want    bool
	}{
		{"exact key", map[string]config.Grant{"tag:ingress": grant("sys.*")}, []string{"tag:ingress"}, "sys.info", "", true},
		{"star key matches every tag", map[string]config.Grant{"tag:*": grant("sys.*")}, []string{"tag:ingress"}, "sys.info", "", true},
		{"star key does not widen the tool list", map[string]config.Grant{"tag:*": grant("sys.*")}, []string{"tag:ingress"}, "exec.run", "", false},
		{"prefix key", map[string]config.Grant{"tag:ingress-*": grant("sys.*")}, []string{"tag:ingress-eu"}, "sys.info", "", true},
		{"prefix key needs the prefix", map[string]config.Grant{"tag:ingress-*": grant("sys.*")}, []string{"tag:ingress"}, "sys.info", "", false},
		{"key does not match another namespace", map[string]config.Grant{"tag:*": grant("*")}, []string{"group:admin"}, "sys.info", "", false},
		{"untagged caller is denied", map[string]config.Grant{"tag:*": grant("*")}, nil, "sys.info", "", false},
		{"no entry is denied", map[string]config.Grant{}, []string{"tag:ingress"}, "sys.info", "", false},
		{"empty tools denies", map[string]config.Grant{"tag:*": {Backends: []string{"*"}, Tools: []string{}}}, []string{"tag:ingress"}, "sys.info", "", false},
		{"grants union across matching keys", map[string]config.Grant{"tag:*": grant("sys.*"), "tag:ingress": grant("fs.*")}, []string{"tag:ingress"}, "fs.read", "", true},
		{"second tag can match", map[string]config.Grant{"tag:ingress": grant("fs.*")}, []string{"tag:device", "tag:ingress"}, "fs.read", "", true},
		{"backend through a star key", map[string]config.Grant{"tag:*": grant("ida.*")}, []string{"tag:ingress"}, "", "ida", true},
		{"other backend stays denied", map[string]config.Grant{"tag:*": grant("*")}, []string{"tag:ingress"}, "", "other", false},
	}
	for _, c := range cases {
		cfg := &config.Config{}
		cfg.Agent.Authz = c.authz
		if got := Allowed(cfg, identity.Caller{Tags: c.tags}, c.tool, c.backend); got != c.want {
			t.Fatalf("%s: Allowed = %v", c.name, got)
		}
	}
	cfg := &config.Config{}
	cfg.Agent.Authz = map[string]config.Grant{"tag:*": {Tools: []string{"ida.*"}}}
	if !backendToolAllowed(cfg, identity.Caller{Tags: []string{"tag:ingress"}}, "ida") || backendToolAllowed(cfg, identity.Caller{Tags: []string{"group:x"}}, "ida") || backendToolAllowed(cfg, identity.Caller{}, "ida") {
		t.Fatal("backend tool grant through a star key")
	}
}

func rewrite(t *testing.T, path string, edit func(agent map[string]any)) {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	edit(doc["agent"].(map[string]any))
	b, _ = json.Marshal(doc)
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}

func Test_CONSEC10_QuotaFromConfig(t *testing.T) {
	path, _ := configuration(t)
	rewrite(t, path, func(agent map[string]any) {
		// A wildcard key keeps the Local caller authorised end to end.
		agent["authz"] = map[string]any{"tag:*": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}
		agent["quota"] = map[string]any{"calls_per_hour": 2}
	})
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	if hourly, concurrent := a.quota.Hourly, a.quota.Concurrent; hourly != 2 || concurrent != config.DefaultMaxConcurrent {
		t.Fatal("quota not read from configuration", hourly, concurrent)
	}
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	for i := range 2 {
		if out := tool(t, client, "pty.list", map[string]any{}); out.IsError {
			t.Fatal(i, out.Content)
		}
	}
	if out := tool(t, client, "pty.list", map[string]any{}); !out.IsError || object(t, out)["rule"] != "quota.exceeded" {
		t.Fatal("configured hourly budget not enforced", out)
	}
	// Quota numbers hot-load: the next call uses the new budget.
	rewrite(t, path, func(agent map[string]any) { agent["quota"] = map[string]any{"calls_per_hour": 100} })
	deadline := time.Now().Add(3 * time.Second)
	for {
		out := tool(t, client, "pty.list", map[string]any{})
		if !out.IsError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("raised quota never took effect", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a.quota.Hourly != 100 {
		t.Fatal(a.quota.Hourly)
	}
}

func Test_CONSEC10_ExecTimeoutNotSilentlyTruncated(t *testing.T) {
	path, _ := configuration(t)
	rewrite(t, path, func(agent map[string]any) {
		agent["quota"] = map[string]any{"max_call_duration": "3s"}
		agent["exec"] = map[string]any{"default_timeout": "2s"}
	})
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	run := func(timeout any) map[string]any {
		args := map[string]any{"cmd": "printf", "args": []string{"ok"}}
		if runtime.GOOS == "windows" {
			args = map[string]any{"cmd": "powershell", "args": []string{"-NoProfile", "-Command", "Write-Output ok"}}
		}
		if timeout != nil {
			args["timeout"] = timeout
		}
		return object(t, tool(t, client, "exec.run", args))
	}
	for _, timeout := range []any{"10m", "4s", 4000, 3600000, int64(1) << 60} {
		out := run(timeout)
		if out["error"] != "invalid_args" || out["field"] != "timeout" || out["reason"] != "quota.max_call_duration" || out["limit"] != float64(3000) {
			t.Fatal("over-long timeout was not refused", timeout, out)
		}
	}
	for _, timeout := range []any{nil, "2s", 3000, "3s"} {
		if out := run(timeout); out["error"] != nil {
			t.Fatal("timeout within the budget refused", timeout, out)
		}
	}
}

func Test_CONSEC10_RefusedExecDoesNotSpendBudget(t *testing.T) {
	path, _ := configuration(t)
	rewrite(t, path, func(agent map[string]any) {
		agent["quota"] = map[string]any{"calls_per_hour": 1, "max_call_duration": "3s"}
		agent["exec"] = map[string]any{"default_timeout": "2s"}
	})
	store, _ := config.NewStore(path)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if out := tool(t, client, "exec.run", map[string]any{"cmd": "x", "timeout": "1h"}); !out.IsError || object(t, out)["error"] != "invalid_args" {
		t.Fatal(out)
	}
	if out := tool(t, client, "pty.list", map[string]any{}); out.IsError {
		t.Fatal("a refused call consumed the hourly budget", out.Content)
	}
}
