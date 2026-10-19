package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func TestDesktopImagesAndParameterScopesThroughIngress(t *testing.T) {
	s := newScripted()
	var opened atomic.Int32
	var backendCalls atomic.Int32
	pixel := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	s.tool("computer.erase", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		backendCalls.Add(1)
		return agent.Result(map[string]any{"erased": true}, nil), nil
	})
	s.tool("computer.open", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opened.Add(1)
		return agent.Result(map[string]any{"session_id": "cs"}, nil), nil
	})
	s.tool("computer.observe", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r := agent.Result(map[string]any{"frame_id": "f"}, nil)
		r.Content = append(r.Content, &mcp.ImageContent{MIMEType: "image/png", Data: pixel})
		return r, nil
	})
	g := New(Options{NodeName: "ingress", Ingress: true, LocalPeer: mesh.Peer{Name: "desktop", ID: "desktop", Online: true}, Dial: func(ctx context.Context, _ mesh.Peer, _ Principal, options mcpmux.Options) (*mcpmux.Bridge, error) {
		one, two := mcp.NewInMemoryTransports()
		if _, err := s.server.Connect(ctx, one, nil); err != nil {
			return nil, err
		}
		return mcpmux.Connect(ctx, two, options)
	}})
	t.Cleanup(g.Close)
	ts := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "viewer", Scopes: []string{"devices:read", "computer:observe:desktop"}}, nil
	}))
	t.Cleanup(ts.Close)
	c := connect(t, ts.URL+"/mcp")
	waitFor(t, "computer discovery", 3e9, func() bool { return hasTool(t, c, "desktop/computer.open") })
	if hasTool(t, c, "desktop/computer.erase") {
		t.Fatal("observation scope exposed backend tool")
	}
	denied := call(t, c, "desktop/computer.erase", map[string]any{})
	if !denied.IsError || backendCalls.Load() != 0 {
		t.Fatal("observation token dispatched a backend side effect", denied, backendCalls.Load())
	}
	r := call(t, c, "desktop/computer.open", map[string]any{"mode": "control"})
	if !r.IsError || object(t, r)["error"] != "unauthorized" || opened.Load() != 0 {
		t.Fatal(r, opened.Load())
	}
	r = call(t, c, "desktop/computer.open", map[string]any{"mode": "observe"})
	if r.IsError || opened.Load() != 1 {
		t.Fatal(r, opened.Load())
	}
	r = call(t, c, "desktop/computer.observe", map[string]any{})
	if r.IsError || len(r.Content) != 2 {
		t.Fatal(r)
	}
	img, ok := r.Content[1].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, pixel) {
		t.Fatal(r.Content)
	}
	if object(t, r)["frame_id"] != "f" {
		t.Fatal(r)
	}
}
