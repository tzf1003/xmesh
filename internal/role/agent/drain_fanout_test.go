package agent

import (
	"context"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type drainNode struct {
	peer    mesh.Peer
	agent   *Agent
	mu      sync.Mutex
	drained []string
}

func newDrainNode(t *testing.T, name string, tags ...string) *drainNode {
	t.Helper()
	_, store := configuration(t)
	c := *store.Get()
	c.Node.Name = name
	a, e := New(config.Static(&c))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	n := &drainNode{agent: a}
	mux := http.NewServeMux()
	mux.Handle("/control/drain", a.Control(func(*http.Request) (Principal, error) {
		return Principal{Node: "admin", Tags: tags}, nil
	}, func(device string) {
		n.mu.Lock()
		n.drained = append(n.drained, device)
		n.mu.Unlock()
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	n.peer = mesh.Peer{Name: name, ID: name, Address: server.URL + "/mcp", Tags: []string{"tag:device"}, Online: true}
	return n
}

func Test_CTRGW05_MeshDrainFanout(t *testing.T) {
	one := newDrainNode(t, "one", "tag:admin")
	two := newDrainNode(t, "two", "tag:admin")
	// This node does not trust the caller as an administrator.
	three := newDrainNode(t, "three", "tag:peer")
	peers := []mesh.Peer{three.peer, one.peer, two.peer}
	client := func(mesh.Peer) *http.Client { return http.DefaultClient }
	out := FanoutDrain(context.Background(), peers, client, DrainRequest{Scope: "mesh", Drain: "keep"}, 5*time.Second)
	if len(out) != 3 || out[0].Device != "one" || out[1].Device != "three" || out[2].Device != "two" {
		t.Fatal("outcomes are not ordered by device", out)
	}
	if out[0].Err != nil || out[2].Err != nil {
		t.Fatal("administrator drain failed", out)
	}
	if out[1].Err == nil || !strings.Contains(out[1].Err.Error(), "403") {
		t.Fatal("a non-administrator caller was accepted", out[1])
	}
	for _, n := range []*drainNode{one, two} {
		if !n.agent.draining.Load() {
			t.Fatal(n.peer.Name, "agent did not drain")
		}
		n.mu.Lock()
		// A mesh-scoped message makes that node's own Gateway stop routing to everything.
		if len(n.drained) != 1 || n.drained[0] != "" {
			t.Fatal(n.peer.Name, "gateway callback", n.drained)
		}
		n.mu.Unlock()
	}
	if three.agent.draining.Load() {
		t.Fatal("rejected request still drained the node")
	}
	// One unreachable device does not stop the others.
	gone := mesh.Peer{Name: "gone", Address: "http://127.0.0.1:1/mcp", Tags: []string{"tag:device"}}
	out = FanoutDrain(context.Background(), []mesh.Peer{gone, one.peer}, client, DrainRequest{Scope: "mesh", Drain: "kill"}, 2*time.Second)
	if out[0].Device != "gone" || out[0].Err == nil || out[1].Err != nil {
		t.Fatal(out)
	}
}

func Test_CTRGW05_DrainScopeDeviceNamesTheTarget(t *testing.T) {
	one := newDrainNode(t, "one", "tag:admin")
	if err := SendDrain(context.Background(), http.DefaultClient, one.peer, DrainRequest{Scope: "device", Device: "other"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal("a drain for another device was applied", err)
	}
	if one.agent.draining.Load() {
		t.Fatal("misdirected drain applied")
	}
	if err := SendDrain(context.Background(), http.DefaultClient, one.peer, DrainRequest{Scope: "device", Device: "one"}); err != nil {
		t.Fatal(err)
	}
	if !one.agent.draining.Load() {
		t.Fatal("drain not applied")
	}
	one.mu.Lock()
	defer one.mu.Unlock()
	if len(one.drained) != 1 || one.drained[0] != "one" {
		t.Fatal(one.drained)
	}
}
