package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// The gateway reads sys.info from each online device on its own, so the control channel can tell a
// device that has not been read yet from one whose read failed.
func Test_DeviceInfoReadInBackground(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	devices := map[string]*mcp.Server{}
	answers := map[string]func() *mcp.CallToolResult{
		"good": func() *mcp.CallToolResult {
			if healthy.Load() {
				return agent.Result(map[string]any{"os": "linux"}, nil)
			}
			return agent.Result(nil, toolerr.New("internal", "boom"))
		},
		"bad": func() *mcp.CallToolResult { return agent.Result(nil, toolerr.New("internal", "boom")) },
	}
	for name, answer := range answers {
		s := mcp.NewServer(&mcp.Implementation{Name: name, Version: "1"}, nil)
		s.AddTool(&mcp.Tool{Name: "sys.info", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return answer(), nil
		})
		devices[name] = s
	}
	dials := map[string]*atomic.Int32{"good": {}, "bad": {}, "down": {}, "local": {}, "gone": {}}
	dial := func(ctx context.Context, peer mesh.Peer, _ Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		dials[peer.Name].Add(1)
		if peer.Name == "down" {
			return nil, fmt.Errorf("拨号失败")
		}
		one, two := mcp.NewInMemoryTransports()
		if _, e := devices[peer.Name].Connect(ctx, one, nil); e != nil {
			return nil, e
		}
		return mcpmux.Connect(ctx, two, opts)
	}
	discover := func(context.Context) ([]mesh.Peer, error) {
		return []mesh.Peer{
			{Name: "local", ID: "self", Online: true},
			{Name: "good", ID: "good", Online: true},
			{Name: "bad", ID: "bad", Online: true},
			{Name: "down", ID: "down", Online: true},
			{Name: "gone", ID: "gone", Online: false},
		}, nil
	}
	g := New(Options{NodeName: "local", LocalPeer: mesh.Peer{Name: "local", ID: "self", Online: true}, Dial: dial, Discover: discover})
	t.Cleanup(g.Close)
	waitFor(t, "devices", 5*time.Second, func() bool {
		found, _ := g.snapshot()
		return len(found) == 5
	})
	settled := func(name string) func() bool {
		return func() bool {
			r, ok := g.DeviceInfo()[name]
			return ok && (!r.RefreshedAt.IsZero() || r.Error != "")
		}
	}

	now := time.Now()
	g.pollInfo(now)
	waitFor(t, "first readings", 10*time.Second, func() bool { return settled("good")() && settled("bad")() && settled("down")() })
	info := g.DeviceInfo()
	if good := info["good"]; good.RefreshedAt.IsZero() || good.Error != "" {
		t.Fatalf("good device: %+v", good)
	}
	if bad := info["bad"]; !bad.RefreshedAt.IsZero() || !strings.Contains(bad.Error, "sys.info") {
		t.Fatalf("bad device: %+v", bad)
	}
	if down := info["down"]; !strings.Contains(down.Error, "拨号失败") {
		t.Fatalf("unreachable device: %+v", down)
	}
	if _, ok := info["local"]; ok {
		t.Fatal("the node read itself")
	}
	if _, ok := info["gone"]; ok {
		t.Fatal("an offline device was read")
	}
	firstSuccess := info["good"].RefreshedAt

	// a reading stays current for infoEvery: asking again straight away starts no read
	g.pollInfo(now.Add(time.Second))
	g.infoWorkers.Wait()
	if dials["good"].Load() != 1 || dials["down"].Load() != 1 {
		t.Fatalf("re-read too early: good %d, down %d", dials["good"].Load(), dials["down"].Load())
	}

	// a failure after a success keeps the time of the success and says why the latest read failed
	healthy.Store(false)
	g.pollInfo(now.Add(2 * infoEvery))
	waitFor(t, "second readings", 10*time.Second, func() bool { return g.DeviceInfo()["good"].Error != "" })
	if good := g.DeviceInfo()["good"]; !good.RefreshedAt.Equal(firstSuccess) || !strings.Contains(good.Error, "sys.info") {
		t.Fatalf("after a failure: %+v", good)
	}

	// recovering clears the error
	healthy.Store(true)
	g.pollInfo(now.Add(4 * infoEvery))
	waitFor(t, "recovery", 10*time.Second, func() bool {
		return g.DeviceInfo()["good"].Error == "" && g.DeviceInfo()["good"].RefreshedAt.After(firstSuccess)
	})
}
