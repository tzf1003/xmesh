package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// conformance: CTR-GW-02 CTR-GW-04
func Test_DefaultRemoteDeviceToolsWaitForDiscovery(t *testing.T) {
	for _, cancelList := range []bool{false, true} {
		name := "complete"
		if cancelList {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			g, _ := fixture(t, false)
			original := g.opts.Dial
			started, release := make(chan struct{}), make(chan struct{})
			var startOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			g.opts.Dial = func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
				if peer.Name == "remote" {
					startOnce.Do(func() { close(started) })
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return original(ctx, peer, p, opts)
			}
			g.Update([]mesh.Peer{{ID: "remote", Name: "remote", Address: g.opts.LocalPeer.Address}})
			server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
			t.Cleanup(server.Close)
			client := connect(t, server.URL+"/mcp")
			if out := call(t, client, "session.set_defaults", map[string]any{"device": "remote"}); out.IsError {
				t.Fatal(out)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("remote discovery did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type response struct {
				list *mcp.ListToolsResult
				err  error
			}
			done := make(chan response, 1)
			go func() {
				list, err := client.ListTools(ctx, nil)
				done <- response{list, err}
			}()
			select {
			case result := <-done:
				t.Fatalf("tools/list returned before default discovery: %v", result)
			case <-time.After(100 * time.Millisecond):
			}
			if cancelList {
				cancel()
				select {
				case result := <-done:
					if result.err == nil {
						t.Fatal("canceled tools/list succeeded")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("tools/list did not honor cancellation")
				}
			}
			unblock()
			var result response
			if cancelList {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result.list, result.err = client.ListTools(ctx, nil)
			} else {
				select {
				case result = <-done:
				case <-ctx.Done():
					t.Fatal("tools/list did not finish discovery")
				}
			}
			if result.err != nil {
				t.Fatal(result.err)
			}
			seen := map[string]*mcp.Tool{}
			for _, tool := range result.list.Tools {
				seen[tool.Name] = tool
			}
			for _, want := range []string{"local/sys.info", "remote/fs.read", "remote/fs.write", "remote/exec.run", "remote/pty.open", "remote/sys.info"} {
				if tool := seen[want]; tool == nil || tool.InputSchema == nil {
					t.Errorf("default catalogue missing %s or its schema", want)
				}
			}
		})
	}
}

// conformance: CTR-GW-02 CTR-GW-04
func Test_DefaultDeviceDiscoveryReplacement(t *testing.T) {
	for _, change := range []string{"address", "identity"} {
		t.Run(change, func(t *testing.T) {
			g, _ := fixture(t, false)
			original := g.opts.Dial
			started, replacementStarted := make(chan struct{}), make(chan struct{})
			releaseOld, releaseNew := make(chan struct{}), make(chan struct{})
			var oldOnce, newOnce sync.Once
			unblockOld := func() { oldOnce.Do(func() { close(releaseOld) }) }
			unblockNew := func() { newOnce.Do(func() { close(releaseNew) }) }
			defer unblockOld()
			defer unblockNew()
			initial := mesh.Peer{ID: "remote", Name: "remote", Address: "http://initial.invalid/mcp"}
			g.opts.Dial = func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
				if peer.Name != "remote" {
					return original(ctx, peer, p, opts)
				}
				if peer.ID == initial.ID && peer.Address == initial.Address {
					close(started)
					<-ctx.Done()
					<-releaseOld
					return nil, ctx.Err()
				}
				close(replacementStarted)
				select {
				case <-releaseNew:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				peer.Address = g.opts.LocalPeer.Address
				return original(ctx, peer, p, opts)
			}
			g.Update([]mesh.Peer{initial})
			server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
			t.Cleanup(server.Close)
			client := connect(t, server.URL+"/mcp")
			if out := call(t, client, "session.set_defaults", map[string]any{"device": "remote"}); out.IsError {
				t.Fatal(out)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("initial discovery did not start")
			}
			type response struct {
				list *mcp.ListToolsResult
				err  error
			}
			done := make(chan response, 1)
			go func() {
				list, err := client.ListTools(ctx, nil)
				done <- response{list, err}
			}()
			select {
			case result := <-done:
				t.Fatalf("tools/list returned during initial discovery: %v", result)
			case <-time.After(100 * time.Millisecond):
			}
			replacement := initial
			if change == "address" {
				replacement.Address = "http://replacement.invalid/mcp"
			} else {
				replacement.ID = "replacement"
			}
			g.Update([]mesh.Peer{replacement})
			select {
			case <-replacementStarted:
			case <-ctx.Done():
				t.Fatal("replacement discovery did not start")
			}
			unblockOld()
			select {
			case result := <-done:
				t.Fatalf("tools/list returned before replacement discovery: %v", result)
			case <-time.After(100 * time.Millisecond):
			}
			unblockNew()
			select {
			case result := <-done:
				if result.err != nil {
					t.Fatal(result.err)
				}
				for _, tool := range result.list.Tools {
					if tool.Name == "remote/sys.info" && tool.InputSchema != nil {
						return
					}
				}
				t.Fatal("replacement catalogue missing remote/sys.info")
			case <-ctx.Done():
				t.Fatal("tools/list did not finish replacement discovery")
			}
		})
	}
}

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
