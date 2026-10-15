package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

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
