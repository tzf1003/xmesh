package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test_DefaultDeviceToolsListed covers CTR-GW-02: tools/list keeps the
// default device's whole table (schemas included), so a client that set the
// default does not have to guess names through tools.search.
func Test_DefaultDeviceToolsListed(t *testing.T) {
	g, _ := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if o := call(t, client, "session.set_defaults", map[string]any{"device": "local"}); o.IsError {
		t.Fatal(o)
	}
	list, e := client.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, tool := range list.Tools {
		seen[tool.Name] = true
		if tool.Name == "local/fs.read" {
			if tool.InputSchema == nil {
				t.Fatal("fs.read listed without schema")
			}
			if props, ok := tool.InputSchema.(map[string]any)["properties"].(map[string]any); !ok || props["path"] == nil {
				t.Fatalf("fs.read schema lacks path: %v", tool.InputSchema)
			}
		}
	}
	for _, want := range []string{"local/fs.read", "local/fs.write", "local/exec.run", "local/pty.open", "local/sys.info"} {
		if !seen[want] {
			t.Fatalf("default device tool %s missing from tools/list", want)
		}
	}
}

// Test_SearchStarListsEverything covers CTR-GW-03: pattern "*" enumerates the
// full table without brute-forcing single characters.
func Test_SearchStarListsEverything(t *testing.T) {
	g, _ := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	o := call(t, client, "tools.search", map[string]any{"device": "local", "pattern": "*"})
	if o.IsError {
		t.Fatal(o)
	}
	body := object(t, o)
	tools, _ := body["tools"].([]any)
	if len(tools) < 10 {
		t.Fatalf("star search returned %d tools", len(tools))
	}
	for _, raw := range tools {
		row, _ := raw.(map[string]any)
		name, _ := row["name"].(string)
		if !strings.Contains(name, "/") {
			continue
		}
		if row["inputSchema"] == nil {
			t.Fatalf("%s missing inputSchema", name)
		}
	}
}

func Test_DefaultDeviceNormalizesLocalAlias(t *testing.T) {
	g, _ := fixture(t, false)
	v := g.newView(Principal{Local: true})
	defer v.close()
	v.mu.Lock()
	v.defaults["device"] = []byte(`"local"`)
	got := v.defaultDeviceLocked()
	v.mu.Unlock()
	if got != g.opts.LocalPeer.Name {
		t.Fatalf("local alias: %q", got)
	}
}
