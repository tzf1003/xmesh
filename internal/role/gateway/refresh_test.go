package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// conformance: CTR-GW-04
func Test_SlowPeerDoesNotBlockReadyDevices(t *testing.T) {
	g, _ := fixture(t, false)
	original := g.opts.Dial
	started, stopped := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	g.opts.Dial = func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		if peer.Name == "slow" {
			if attempts.Add(1) == 1 {
				close(started)
			}
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		}
		return original(ctx, peer, p, opts)
	}
	g.Update([]mesh.Peer{{ID: "slow", Name: "slow", Address: "http://slow.invalid/mcp"}, {ID: "healthy", Name: "healthy", Address: g.opts.LocalPeer.Address}})
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow peer was not scheduled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for range 3 {
		if _, err := client.ListTools(ctx, nil); err != nil {
			t.Fatal("slow peer blocked catalogue", err)
		}
		for _, name := range []string{"local/sys.info", "healthy/sys.info", "devices.list"} {
			out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			if err != nil || out.IsError {
				t.Fatal(name, out, err)
			}
		}
	}
	if attempts.Load() != 1 {
		t.Fatal("overlapping peer refreshes", attempts.Load())
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("session close did not cancel pending discovery")
	}
}

func Test_ToolChangeDuringRefreshIsRetried(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reading, release, changed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var reads atomic.Int32
			blockAt := int32(1)
			if cached {
				blockAt = 2
			}
			server := mcp.NewServer(&mcp.Implementation{Name: "changing-agent", Version: "1"}, nil)
			server.AddTool(&mcp.Tool{Name: "sys.info", InputSchema: map[string]any{"type": "object"}}, func(callCtx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if reads.Add(1) == blockAt {
					close(reading)
					select {
					case <-release:
					case <-callCtx.Done():
						return nil, callCtx.Err()
					}
				}
				return &mcp.CallToolResult{StructuredContent: map[string]any{"hostname": "device"}}, nil
			})
			g := New(Options{NodeName: "device", LocalPeer: mesh.Peer{ID: "local", Name: "device", Online: true}, Dial: func(dialCtx context.Context, _ mesh.Peer, _ Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
				upstream, downstream := mcp.NewInMemoryTransports()
				if _, err := server.Connect(ctx, upstream, nil); err != nil {
					return nil, err
				}
				notify := opts.Changed
				opts.Changed = func() {
					notify()
					select {
					case changed <- struct{}{}:
					default:
					}
				}
				return mcpmux.Connect(dialCtx, downstream, opts)
			}})
			t.Cleanup(g.Close)
			v := g.newView(Principal{Local: true})
			t.Cleanup(v.close)
			v.refresh()
			if cached {
				v.waitReady(ctx, "device")
				v.mu.Lock()
				v.down["device"].updated = time.Time{}
				v.mu.Unlock()
				v.refresh()
			}
			select {
			case <-reading:
			case <-ctx.Done():
				t.Fatal("refresh did not read sys.info")
			}
			server.AddTool(&mcp.Tool{Name: "ida.echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}, nil
			})
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("tool change was not delivered")
			}
			// Finish processing the change while the old tools snapshot is still in flight.
			v.refresh()
			unblock()
			for {
				v.mu.Lock()
				found := false
				for _, name := range v.names {
					found = found || name == "local/ida.echo"
				}
				v.mu.Unlock()
				if found {
					return
				}
				select {
				case <-ctx.Done():
					t.Fatal("tool change during refresh was lost")
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
}

// conformance: CTR-GW-04
func Test_PeerRetryBackoffAndReplacement(t *testing.T) {
	g, _ := fixture(t, false)
	original := g.opts.Dial
	attempts := make(chan mesh.Peer, 10)
	g.opts.Dial = func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		if peer.Name == "unreachable" {
			attempts <- peer
			return nil, fmt.Errorf("peer unavailable")
		}
		return original(ctx, peer, p, opts)
	}
	peer := mesh.Peer{ID: "first", Name: "unreachable", Address: "http://first.invalid/mcp"}
	g.Update([]mesh.Peer{peer})
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	select {
	case <-attempts:
	case <-time.After(2 * time.Second):
		t.Fatal("peer not attempted")
	}
	// Wait for the failed attempt to publish its cooldown.
	g.mu.Lock()
	var v *view
	for candidate := range g.views {
		v = candidate
	}
	g.mu.Unlock()
	v.waitReady(context.Background(), "unreachable")
	for range 5 {
		if _, err := client.ListTools(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case p := <-attempts:
		t.Fatal("request bypassed retry backoff", p.ID)
	default:
	}
	peer.ID, peer.Address = "replacement", "http://replacement.invalid/mcp"
	g.Update([]mesh.Peer{peer})
	select {
	case p := <-attempts:
		if p.ID != "replacement" {
			t.Fatal("stale peer retried", p.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement inherited old peer backoff")
	}
}
