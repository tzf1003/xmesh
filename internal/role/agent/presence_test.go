package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/identity"
)

type nodeHeader struct{ node string }

func (h nodeHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Test-Node", h.node)
	return http.DefaultTransport.RoundTrip(r)
}

func connectAs(t *testing.T, url, node string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: nodeHeader{node}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

type fakePresence struct {
	mu            sync.Mutex
	online        map[string]bool
	authoritative bool
}

func (f *fakePresence) set(authoritative bool, nodes ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authoritative, f.online = authoritative, map[string]bool{}
	for _, n := range nodes {
		f.online[n] = true
	}
}
func (f *fakePresence) poll(context.Context) (map[string]bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for k := range f.online {
		out[k] = true
	}
	return out, f.authoritative, nil
}

func ptyStates(a *Agent) map[string]string {
	states := map[string]string{}
	for _, o := range a.PTY.List() {
		owner, _ := o["owner"].(string)
		states[o["id"].(string)] = o["state"].(string) + ":" + owner
	}
	return states
}

func presenceFixture(t *testing.T) (*Agent, *httptest.Server) {
	t.Helper()
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	a.SetNetwork("tailnet", Principal{Node: "stable-self", Tags: []string{"tag:device"}})
	server := httptest.NewServer(a.HTTP(func(r *http.Request) (Principal, error) {
		return Principal{Node: r.Header.Get("X-Test-Node"), Tags: []string{"tag:peer"}}, nil
	}))
	t.Cleanup(server.Close)
	return a, server
}
func openPTY(t *testing.T, server *httptest.Server, node string) string {
	t.Helper()
	client := connectAs(t, server.URL+"/mcp", node)
	return object(t, tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24}))["session_id"].(string)
}

func Test_CTRID01_PeerLeaveOrphans(t *testing.T) {
	a, server := presenceFixture(t)
	idA, idB := openPTY(t, server, "node-a"), openPTY(t, server, "node-b")
	presence := &fakePresence{}
	presence.set(true, "node-a", "node-b", "stable-self")
	a.WatchPeers(presence.poll, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if ptyStates(a)[idA] == "orphaned:" {
		t.Fatal("online node lost its PTY")
	}
	// An authoritative listing without node-a ends its sessions; node-b is untouched.
	presence.set(true, "node-b", "stable-self")
	waitFor(t, "node-a PTY orphaned", func() bool { return ptyStates(a)[idA] == "orphaned:" })
	if ptyStates(a)[idB] == "orphaned:" {
		t.Fatal("remaining node lost its PTY")
	}
	waitFor(t, "node-a caller removed", func() bool {
		for id := range a.Callers.Budgets() {
			if identity.Node(id) == "node-a" {
				return false
			}
		}
		return true
	})
	if a.PeerLeft("node-a") != 0 {
		t.Fatal("departed node still has sessions")
	}
}

func Test_CTRID01_PartialListingOnlyEndsObservedNodes(t *testing.T) {
	a, server := presenceFixture(t)
	idSeen, idUnseen := openPTY(t, server, "node-seen"), openPTY(t, server, "node-unseen")
	presence := &fakePresence{}
	presence.set(false, "node-seen", "stable-self")
	a.WatchPeers(presence.poll, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	// Not authoritative and never observed: the node may simply not advertise (LAN).
	presence.set(false, "stable-self")
	waitFor(t, "observed node PTY orphaned", func() bool { return ptyStates(a)[idSeen] == "orphaned:" })
	time.Sleep(150 * time.Millisecond)
	if ptyStates(a)[idUnseen] == "orphaned:" {
		t.Fatal("unobserved node orphaned from a partial listing")
	}
}

func Test_CTRID01_PresenceErrorOrphansNobody(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(func(r *http.Request) (Principal, error) {
		return Principal{Node: "node-a", Tags: []string{"tag:peer"}}, nil
	}))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	id := object(t, tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24}))["session_id"].(string)
	a.WatchPeers(func(context.Context) (map[string]bool, bool, error) {
		return nil, true, errors.New("control plane unreachable")
	}, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	if ptyStates(a)[id] == "orphaned:" {
		t.Fatal("presence error orphaned a PTY")
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for", what)
}
