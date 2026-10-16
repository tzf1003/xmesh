package gateway

import (
	"github.com/tzf1003/xmesh/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func Test_CONSEC10_GatewayQuotaFromConfig(t *testing.T) {
	g := New(Options{NodeName: "test-node", Quota: config.Quota{CallsPerHour: 2, MaxCallDuration: "3s"}})
	t.Cleanup(g.Close)
	if g.quota.Hourly != 2 || g.quota.Concurrent != config.DefaultMaxConcurrent || g.quota.Duration.Seconds() != 3 {
		t.Fatal("gateway.quota ignored", g.quota.Hourly, g.quota.Concurrent, g.quota.Duration)
	}
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	// A refused exec timeout must not spend budget.
	for _, name := range []string{"local/exec.run", "exec.run"} {
		out := object(t, call(t, client, name, map[string]any{"cmd": "x", "timeout": "1h"}))
		if out["error"] != "invalid_args" || out["reason"] != "quota.max_call_duration" {
			t.Fatal(name, out)
		}
	}
	for i := range 2 {
		if out := call(t, client, "devices.list", map[string]any{}); out.IsError {
			t.Fatal(i, out.Content)
		}
	}
	if out := call(t, client, "devices.list", map[string]any{}); !out.IsError || object(t, out)["rule"] != "quota.exceeded" {
		t.Fatal("configured gateway budget not enforced", out)
	}
	if got := New(Options{}); got.quota.Hourly != 10000 || got.quota.Concurrent != 64 {
		t.Fatal("gateway defaults changed")
	} else {
		got.Close()
	}
}
