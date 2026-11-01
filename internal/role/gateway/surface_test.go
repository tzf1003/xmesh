package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

func surfaceClient(t *testing.T, s Surface) (*mcp.ClientSession, string, func() map[string]map[string]any) {
	t.Helper()
	g, root := fixture(t, false)
	g.SetSurface(s)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	t.Cleanup(func() { _ = client.Close() })
	list := func() map[string]map[string]any {
		out, e := client.ListTools(context.Background(), nil)
		if e != nil {
			t.Fatal(e)
		}
		tools := map[string]map[string]any{}
		for _, tool := range out.Tools {
			tools[tool.Name] = schemaMap(tool)
		}
		return tools
	}
	return client, root, list
}

// conformance: CTR-GW-08
func Test_CTRGW08_UnifiedList(t *testing.T) {
	client, root, list := surfaceClient(t, Surface{Mode: SurfaceUnified})
	tools := list()
	for name := range tools {
		if strings.Contains(name, "/") {
			t.Fatal("prefixed tool listed in unified mode", name)
		}
	}
	for _, name := range []string{"devices.list", "tools.search", "tools.call", "session.set_defaults", "fs.read", "exec.run", "pty.open", "sys.info"} {
		if tools[name] == nil {
			t.Fatal("missing", name)
		}
	}
	for _, name := range []string{"backend.acquire", "computer.act"} {
		if tools[name] != nil {
			t.Fatal("non-listed group present", name)
		}
	}
	props, _ := tools["fs.read"]["properties"].(map[string]any)
	if props["device"] == nil || props["path"] == nil {
		t.Fatal("device argument not injected", tools["fs.read"])
	}
	if required, _ := tools["fs.read"]["required"].([]any); len(required) == 0 {
		t.Fatal("fs.read lost its required list")
	} else {
		for _, r := range required {
			if r == "device" {
				t.Fatal("device must stay optional")
			}
		}
	}
	// No device and no default: invalid_args naming the online devices.
	o := call(t, client, "fs.write", map[string]any{"path": "a", "content": "x"})
	if !o.IsError || object(t, o)["error"] != "invalid_args" || object(t, o)["online"] == nil {
		t.Fatal(object(t, o))
	}
	// An explicit device routes the call.
	o = call(t, client, "fs.write", map[string]any{"device": "local", "path": "a", "content": "hello", "cwd": root})
	if o.IsError {
		t.Fatal(object(t, o))
	}
	if body, err := os.ReadFile(filepath.Join(root, "a")); err != nil || string(body) != "hello" {
		t.Fatal(string(body), err)
	}
	// A session default replaces it.
	if o = call(t, client, "session.set_defaults", map[string]any{"device": "local", "cwd": root}); o.IsError {
		t.Fatal(object(t, o))
	}
	if o = call(t, client, "fs.read", map[string]any{"path": "a"}); o.IsError || !strings.Contains(object(t, o)["content"].(string), "hello") {
		t.Fatal(object(t, o))
	}
}

// conformance: CTR-GW-08
func Test_CTRGW08_CompactListsPinnedOnly(t *testing.T) {
	_, _, list := surfaceClient(t, Surface{Mode: SurfaceCompact, PinnedTools: []string{"exec.run"}})
	tools := list()
	want := map[string]bool{"devices.list": true, "tools.search": true, "tools.call": true, "session.set_defaults": true, "exec.run": true}
	if len(tools) != len(want) {
		t.Fatal(len(tools), tools)
	}
	for name := range tools {
		if !want[name] {
			t.Fatal("unexpected", name)
		}
	}
}

// conformance: CTR-GW-02
func Test_CTRGW02_ToolsCallAndSearch(t *testing.T) {
	for _, mode := range []string{SurfaceFull, SurfaceUnified, SurfaceCompact} {
		t.Run(mode, func(t *testing.T) {
			client, root, _ := surfaceClient(t, Surface{Mode: mode})
			o := call(t, client, "tools.call", map[string]any{"device": "local", "tool": "fs.write", "arguments": map[string]any{"path": filepath.Join(root, "b"), "content": "via"}})
			if o.IsError {
				t.Fatal(object(t, o))
			}
			if body, err := os.ReadFile(filepath.Join(root, "b")); err != nil || string(body) != "via" {
				t.Fatal(string(body), err)
			}
			// The prefixed spelling is accepted as well.
			if o = call(t, client, "tools.call", map[string]any{"tool": "local/fs.read", "arguments": map[string]any{"path": filepath.Join(root, "b")}}); o.IsError {
				t.Fatal(object(t, o))
			}
			// Gateway tools are not valid targets.
			for _, target := range []string{"tools.call", "devices.list", "local/tools.search"} {
				if o = call(t, client, "tools.call", map[string]any{"tool": target}); !o.IsError || object(t, o)["error"] != "invalid_args" {
					t.Fatal(target, object(t, o))
				}
			}
			// A failed call carries the target's schema.
			o = call(t, client, "tools.call", map[string]any{"device": "local", "tool": "fs.read", "arguments": map[string]any{}})
			if !o.IsError || object(t, o)["inputSchema"] == nil {
				t.Fatal(object(t, o))
			}
			// Search across devices: one row per tool, device list, brief by default.
			o = call(t, client, "tools.search", map[string]any{"group": "fs"})
			rows := object(t, o)["tools"].([]any)
			if o.IsError || len(rows) < 10 {
				t.Fatal(object(t, o))
			}
			first := rows[0].(map[string]any)
			if first["inputSchema"] != nil || first["schema_id"] == "" || len(first["devices"].([]any)) != 1 {
				t.Fatal(first)
			}
			o = call(t, client, "tools.search", map[string]any{"names": []string{"fs.read"}, "detail": "schema"})
			rows = object(t, o)["tools"].([]any)
			if len(rows) != 1 || rows[0].(map[string]any)["inputSchema"] == nil {
				t.Fatal(object(t, o))
			}
			if o = call(t, client, "tools.search", map[string]any{}); !o.IsError {
				t.Fatal("empty search accepted")
			}
			// devices.list reports tool groups.
			devices := object(t, call(t, client, "devices.list", map[string]any{}))["devices"].([]any)
			groups := devices[0].(map[string]any)["groups"].(map[string]any)
			if groups["fs"] == nil || groups["pty"] == nil {
				t.Fatal(groups)
			}
		})
	}
}

// conformance: CTR-GW-08
func Test_CTRGW08_FullModeIgnoresDeviceArgument(t *testing.T) {
	client, _, list := surfaceClient(t, Surface{})
	if list()["local/fs.read"] == nil {
		t.Fatal("full list changed")
	}
	// Without a default device an unprefixed name fails as before; device is not special in full mode.
	o := call(t, client, "fs.read", map[string]any{"device": "local", "path": "a"})
	if !o.IsError || object(t, o)["error"] == "invalid_args" && object(t, o)["online"] != nil {
		t.Fatal(object(t, o))
	}
}

// conformance: CTR-GW-08
func Test_CTRGW08_SurfaceQueryOverridesPerSession(t *testing.T) {
	g, _ := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	names := func(url string) map[string]bool {
		out, e := connect(t, url).ListTools(context.Background(), nil)
		if e != nil {
			t.Fatal(e)
		}
		set := map[string]bool{}
		for _, tool := range out.Tools {
			set[tool.Name] = true
		}
		return set
	}
	if full := names(server.URL + "/mcp"); !full["local/fs.read"] || full["fs.read"] {
		t.Fatal("default session is not full", full)
	}
	if unified := names(server.URL + "/mcp?surface=unified"); unified["local/fs.read"] || !unified["fs.read"] {
		t.Fatal("unified override ignored", unified)
	}
	if compact := names(server.URL + "/mcp?surface=compact"); len(compact) != 4 {
		t.Fatal("compact override ignored", compact)
	}
	// The gateway-wide setting is untouched by the override.
	if full := names(server.URL + "/mcp"); !full["local/fs.read"] {
		t.Fatal("override leaked into another session")
	}
	resp, err := http.Post(server.URL+"/mcp?surface=bogus", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("invalid surface accepted", resp.StatusCode)
	}
}

// conformance: CTR-GW-08
func Test_CTRGW08_UnifiedDoesNotWaitForSlowPeers(t *testing.T) {
	defer func(d time.Duration) { listWait = d }(listWait)
	listWait = 1500 * time.Millisecond
	for mode, waits := range map[string]bool{SurfaceFull: true, SurfaceUnified: false, SurfaceCompact: false} {
		t.Run(mode, func(t *testing.T) {
			g, _ := fixture(t, false)
			g.SetSurface(Surface{Mode: mode})
			original := g.opts.Dial
			g.opts.Dial = func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
				if peer.Name == "slow" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return original(ctx, peer, p, opts)
			}
			g.Update([]mesh.Peer{{ID: "slow", Name: "slow", Address: "http://slow.invalid/mcp"}})
			server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
			t.Cleanup(server.Close)
			client := connect(t, server.URL+"/mcp")
			began := time.Now()
			if _, err := client.ListTools(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			took := time.Since(began)
			if waits && took < listWait-200*time.Millisecond {
				t.Fatal("full list did not wait for the slow peer", took)
			}
			if !waits && took >= listWait-200*time.Millisecond {
				t.Fatal("list waited for the slow peer", took)
			}
		})
	}
}
