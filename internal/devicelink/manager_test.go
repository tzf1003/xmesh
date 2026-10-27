package devicelink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
)

type testNode struct {
	id           *device.Identity
	trust        *device.TrustStore
	directory    *device.Directory
	manager      *Manager
	lan, tailnet *httptest.Server
	accepts      atomic.Int32
	calls        atomic.Int32
	started      chan struct{}
	release      chan struct{}
	dialGate     <-chan struct{}
	order        atomic.Pointer[[]device.PathKind]
	smart        atomic.Bool
	switched     chan pathSwitch
}

func newTestNode(t *testing.T, name string) *testNode {
	t.Helper()
	id, err := device.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := device.OpenTrust(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := device.NewDirectory(t.Context(), trust, id.ID)
	if err := directory.SetProvider(device.LAN, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := directory.SetProvider(device.Tailnet, 1, true); err != nil {
		t.Fatal(err)
	}
	n := &testNode{id: id, trust: trust, directory: directory, switched: make(chan pathSwitch, 16)}
	manager, err := NewManager(t.Context(), Options{
		Identity: id, Trust: trust, Directory: directory, BootID: name + "-boot", RecoveryWindow: 2 * time.Second, PathOrder: n.pathOrder, Smart: n.smart.Load, Switching: testSwitching, OnPathSwitch: n.onSwitch, Heartbeat: 100 * time.Millisecond, DeadAfter: time.Second,
		Dial: func(ctx context.Context, target string, path device.Path) (*websocket.Conn, error) {
			if path.Kind == device.Tailnet && n.dialGate != nil {
				select {
				case <-n.dialGate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: trust.TLS(id, target, false)}}
			conn, _, err := websocket.Dial(ctx, strings.Replace(path.Address, "https://", "wss://", 1)+LinkPath, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{Subprotocol}})
			return conn, err
		},
		Accept: func(ctx context.Context, p identity.Principal, s *Session) error {
			n.accepts.Add(1)
			server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "2"}, nil)
			server.AddTool(&mcp.Tool{Name: "count.once", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				n.calls.Add(1)
				if n.started != nil {
					close(n.started)
					select {
					case <-n.release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return &mcp.CallToolResult{StructuredContent: map[string]any{"caller": p.Node + "/" + s.SessionID(), "count": n.calls.Load()}}, nil
			})
			_, err := server.Connect(ctx, &Transport{Session: s}, nil)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n.manager = manager
	serve := func(kind device.PathKind) *httptest.Server {
		s := httptest.NewUnstartedServer(manager.Handler(kind))
		s.TLS = trust.TLS(id, "", true)
		s.StartTLS()
		return s
	}
	n.lan, n.tailnet = serve(device.LAN), serve(device.Tailnet)
	t.Cleanup(func() { _ = manager.Close(); directory.Close(); n.lan.Close(); n.tailnet.Close() })
	return n
}

// testSwitching makes smart mode react in milliseconds; a test may widen one knob and must restore it.
var testSwitching = Switching{Every: 20 * time.Millisecond, Dwell: 100 * time.Millisecond, EmergencyDwell: 50 * time.Millisecond, Sustain: 200 * time.Millisecond}

func (n *testNode) onSwitch(id string, from, to device.PathKind, reason string) {
	select {
	case n.switched <- pathSwitch{id: id, from: from, to: to, reason: reason}:
	default:
	}
}

func (n *testNode) pathOrder() []device.PathKind {
	if order := n.order.Load(); order != nil {
		return *order
	}
	return []device.PathKind{device.LAN, device.Tailnet, device.Cloudflare}
}

func (n *testNode) setOrder(kinds ...device.PathKind) { n.order.Store(&kinds) }

func pairTestNodes(t *testing.T, a, b *testNode) {
	t.Helper()
	for _, nodes := range [][2]*testNode{{a, b}, {b, a}} {
		local, remote := nodes[0], nodes[1]
		if err := local.trust.Grant(t.Context(), remote.id.Document("device"), []string{"tag:peer", "tag:device"}, nil); err != nil {
			t.Fatal(err)
		}
		trusted, _ := local.trust.Find(remote.id.ID)
		for kind, url := range map[device.PathKind]string{device.LAN: remote.lan.URL, device.Tailnet: remote.tailnet.URL} {
			now := time.Now()
			if err := local.directory.Observe(remote.id.Certificate.Leaf, trusted.Generation, device.Path{Kind: kind, Address: url, BootID: remote.manager.BootID(), ProviderGeneration: 1, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func openClient(t *testing.T, ctx context.Context, a, b *testNode) *mcp.ClientSession {
	t.Helper()
	transport, err := a.manager.Open(ctx, b.id.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "upstream", Version: "2"}, nil)
	s, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitPath(t *testing.T, m *Manager, id string, kind device.PathKind) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		a := m.Activity(id)
		if a.State == "online" && a.Path.Kind == kind {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("path not reached", m.Activity(id), kind)
}

func TestManagerLANFirstAndStableSessionAcrossTailnetFailover(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	b.started, b.release = make(chan struct{}), make(chan struct{})
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN)
	id := client.ID()
	result := make(chan error, 1)
	go func() {
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "count.once", Arguments: map[string]any{}})
		if err == nil && out.IsError {
			err = errors.New("tool failed")
		}
		result <- err
	}()
	select {
	case <-b.started:
	case <-ctx.Done():
		t.Fatal("request did not execute")
	}
	for _, n := range []*testNode{a, b} {
		if err := n.directory.SetProvider(device.LAN, 2, false); err != nil {
			t.Fatal(err)
		}
		n.manager.DisablePath(device.LAN)
	}
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
	close(b.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("in-flight result lost")
	}
	if b.calls.Load() != 1 || client.ID() != id || b.accepts.Load() != 1 {
		t.Fatal("session rebuilt or request duplicated", b.calls.Load(), b.accepts.Load(), id, client.ID())
	}
	// LAN becomes available again while the Tailnet carrier is healthy.
	for _, nodes := range [][2]*testNode{{a, b}, {b, a}} {
		n, r := nodes[0], nodes[1]
		if err := n.directory.SetProvider(device.LAN, 3, true); err != nil {
			t.Fatal(err)
		}
		trusted, _ := n.trust.Find(r.id.ID)
		now := time.Now()
		if err := n.directory.Observe(r.id.Certificate.Leaf, trusted.Generation, device.Path{Kind: device.LAN, Address: r.lan.URL, BootID: r.manager.BootID(), ProviderGeneration: 3, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if a.manager.Activity(b.id.ID).Path.Kind != device.Tailnet {
		t.Fatal("healthy carrier switched back to LAN")
	}
}

func TestManagerConcurrentBidirectionalOpenUsesOneCarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	done := make(chan error, 2)
	for _, nodes := range [][2]*testNode{{a, b}, {b, a}} {
		go func() {
			transport, err := nodes[0].manager.Open(ctx, nodes[1].id.ID, "")
			if err == nil {
				client := mcp.NewClient(&mcp.Implementation{Name: "upstream", Version: "2"}, nil)
				var session *mcp.ClientSession
				session, err = client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
				if session != nil {
					defer session.Close()
				}
			}
			done <- err
		}()
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("bidirectional open blocked")
		}
	}
	aa, ba := a.manager.Activity(b.id.ID), b.manager.Activity(a.id.ID)
	if aa.State != "online" || ba.State != "online" || aa.Epoch != ba.Epoch {
		t.Fatal("different committed carriers", aa, ba)
	}
}

func TestManagerExpiresLogicalSessionsAfterBothPathsFail(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	for _, n := range []*testNode{a, b} {
		for _, kind := range []device.PathKind{device.LAN, device.Tailnet} {
			if err := n.directory.SetProvider(kind, 2, false); err != nil {
				t.Fatal(err)
			}
			n.manager.DisablePath(kind)
		}
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("expired logical session remained active")
	}
	if got := a.manager.Activity(b.id.ID); got.State != "offline" {
		t.Fatal("expired session still reconnecting", got)
	}
}

func TestManagerWaitsForLANDiscoveryBeforeCommittingTailnet(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	for _, nodes := range [][2]*testNode{{a, b}, {b, a}} {
		local, remote := nodes[0], nodes[1]
		if err := local.directory.Lost(remote.id.ID, device.LAN, remote.lan.URL, 1); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		transport, err := a.manager.Open(ctx, b.id.ID, "")
		if transport != nil {
			defer transport.Session.Close()
		}
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	if got := a.manager.Activity(b.id.ID); got.State == "online" {
		t.Fatal("Tailnet committed before LAN discovery budget", got)
	}
	for _, nodes := range [][2]*testNode{{a, b}, {b, a}} {
		local, remote := nodes[0], nodes[1]
		trusted, _ := local.trust.Find(remote.id.ID)
		now := time.Now()
		if err := local.directory.Observe(remote.id.Certificate.Leaf, trusted.Generation, device.Path{Kind: device.LAN, Address: remote.lan.URL, BootID: remote.manager.BootID(), ProviderGeneration: 1, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitPath(t, a.manager, b.id.ID, device.LAN)
}

func TestManagerIdentityConflictEndsActiveSessionAndRejectsNewOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	trusted, _ := a.trust.Find(b.id.ID)
	now := time.Now()
	if err := a.directory.Observe(b.id.Certificate.Leaf, trusted.Generation, device.Path{Kind: device.Tailnet, Address: b.tailnet.URL, BootID: "cloned-process", ProviderGeneration: 1, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.Open(ctx, b.id.ID, ""); !errors.Is(err, ErrIdentityConflict) {
		t.Fatal("conflicted identity accepted", err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("conflicted identity kept its active session")
	}
}

func TestManagerRecoveryDeadlineFencesPendingDial(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	gate := make(chan struct{})
	for _, n := range []*testNode{a, b} {
		n.dialGate = gate
		n.manager.opts.RecoveryWindow = 150 * time.Millisecond
	}
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	for _, n := range []*testNode{a, b} {
		if err := n.directory.SetProvider(device.LAN, 2, false); err != nil {
			t.Fatal(err)
		}
		n.manager.DisablePath(device.LAN)
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending dial extended the recovery deadline")
	}
	close(gate)
	time.Sleep(250 * time.Millisecond)
	if got := a.manager.Activity(b.id.ID); got.State == "online" {
		t.Fatal("late dial committed after recovery expired", got)
	}
}

func TestManagerCleanShutdownEndsSessionsBeforeRecoveryWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	if err := b.manager.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("clean exit kept the remote logical session recoverable")
	}
}

func TestClosedOfflineSessionNotifiesRemoteAfterReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	transport, err := a.manager.Open(ctx, b.id.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []*testNode{a, b} {
		for _, kind := range []device.PathKind{device.LAN, device.Tailnet} {
			if err := n.directory.SetProvider(kind, 2, false); err != nil {
				t.Fatal(err)
			}
			n.manager.DisablePath(kind)
		}
	}
	wait := func(condition func() bool) {
		for deadline := time.Now().Add(3 * time.Second); !condition(); {
			if time.Now().After(deadline) {
				t.Fatal("session state did not settle")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	pa, _ := a.manager.peer(b.id.ID)
	pb, _ := b.manager.peer(a.id.ID)
	wait(func() bool { pa.mu.Lock(); defer pa.mu.Unlock(); return pa.active == nil })
	if err := transport.Session.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	pa.mu.Lock()
	retained := len(pa.sessions)
	pa.mu.Unlock()
	if retained != 1 {
		t.Fatal("authenticated close notification was discarded", retained)
	}
	for _, pair := range [][2]*testNode{{a, b}, {b, a}} {
		local, remote := pair[0], pair[1]
		if err := local.directory.SetProvider(device.LAN, 3, true); err != nil {
			t.Fatal(err)
		}
		trust, _ := local.trust.Find(remote.id.ID)
		now := time.Now()
		if err := local.directory.Observe(remote.id.Certificate.Leaf, trust.Generation, device.Path{Kind: device.LAN, Address: remote.lan.URL, BootID: remote.manager.BootID(), ProviderGeneration: 3, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	wait(func() bool {
		pa.mu.Lock()
		first := len(pa.sessions)
		pa.mu.Unlock()
		pb.mu.Lock()
		second := len(pb.sessions)
		pb.mu.Unlock()
		return first == 0 && second == 0
	})
}

func TestUnattachedIncomingSessionExpiresWithHealthyCarrier(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	transport, err := a.manager.Open(t.Context(), b.id.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Session.Close()
	pa, _ := a.manager.peer(b.id.ID)
	pb, _ := b.manager.peer(a.id.ID)
	// Model a caller that no longer knows the remote session after reconnect.
	pa.mu.Lock()
	delete(pa.sessions, transport.Session.id)
	pa.mu.Unlock()
	pb.mu.Lock()
	remote := pb.sessions[transport.Session.id]
	remote.attachedEpoch = 0
	remote.detachedAt = time.Now().Add(-3 * time.Second)
	pb.signalLocked()
	pb.mu.Unlock()
	for deadline := time.Now().Add(time.Second); !remote.session.Closed(); {
		if time.Now().After(deadline) {
			t.Fatal("healthy carrier retained an abandoned incoming session")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDelegatedSessionsRequireIngressTrust(t *testing.T) {
	for _, grant := range []struct {
		name     string
		tags     []string
		accepted bool
	}{
		{"ordinary-peer", []string{"tag:peer", "tag:device"}, false},
		{"mixed-grants", []string{"tag:peer", "tag:device", "tag:admin", "tag:ingress"}, true},
	} {
		t.Run(grant.name, func(t *testing.T) {
			a, b := newTestNode(t, "a"), newTestNode(t, "b")
			pairTestNodes(t, a, b)
			if err := b.trust.Grant(t.Context(), a.id.Document("sender"), grant.tags, nil); err != nil {
				t.Fatal(err)
			}
			seen := make(chan identity.Principal, 1)
			b.manager.opts.Accept = func(_ context.Context, p identity.Principal, _ *Session) error { seen <- p; return nil }
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			transport, err := a.manager.Open(ctx, b.id.ID, "oauth-client")
			if !grant.accepted {
				if err == nil {
					transport.Session.Close()
					t.Fatal("ordinary peer delegated its broader grant to an OAuth client")
				}
				select {
				case p := <-seen:
					t.Fatal("unauthorized session reached Agent", p)
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Session.Close()
			principal := <-seen
			if principal.Node != a.id.ID || principal.OAuthClient != "oauth-client" || !reflect.DeepEqual(principal.Tags, []string{"tag:ingress"}) {
				t.Fatal("OAuth session inherited non-ingress grants", principal)
			}
		})
	}
}

func TestDelegatedPrincipalMatrix(t *testing.T) {
	for _, item := range []struct {
		tags  []string
		oauth string
		ok    bool
	}{
		{[]string{"tag:peer"}, "", true},
		{[]string{"tag:peer"}, "claimed", false},
		{[]string{"tag:admin", "tag:device"}, "claimed", false},
		{[]string{"tag:ingress", "tag:peer", "tag:admin"}, "client-1", true},
		{[]string{"tag:ingress"}, "", true},
		{[]string{"tag:ingress", "tag:peer"}, "", true},
	} {
		p, err := delegatedPrincipal("device", item.tags, item.oauth)
		if (err == nil) != item.ok {
			t.Fatal(item, p, err)
		}
		if item.ok && item.oauth != "" && (p.OAuthClient != item.oauth || !reflect.DeepEqual(p.Tags, []string{"tag:ingress"})) {
			t.Fatal(item, p)
		}
		if item.ok && item.oauth == "" && (p.OAuthClient != "" || slices.Contains(p.Tags, "tag:ingress")) {
			t.Fatal("session without an OAuth client kept the ingress role", item, p)
		}
	}
}

func TestClosedSessionsReleaseSlotsAfterAcknowledgement(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pa, _ := a.manager.peer(b.id.ID)
	pb, _ := b.manager.peer(a.id.ID)
	for range maxLogicalSessions + 1 {
		transport, err := a.manager.Open(ctx, b.id.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := transport.Session.Close(); err != nil {
			t.Fatal(err)
		}
		for {
			pa.mu.Lock()
			left := len(pa.sessions)
			pa.mu.Unlock()
			pb.mu.Lock()
			right := len(pb.sessions)
			pb.mu.Unlock()
			if left == 0 && right == 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("close acknowledgement retained a session slot", left, right)
			case <-time.After(time.Millisecond):
			}
		}
	}
}

func TestManagerFollowsConfiguredPathOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	for _, n := range []*testNode{a, b} {
		n.setOrder(device.Tailnet, device.LAN)
	}
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
}

func TestManagerNeverUsesDisabledPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	for _, n := range []*testNode{a, b} {
		n.setOrder(device.Tailnet)
	}
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
	if paths := a.manager.candidates(b.id.ID); len(paths) != 1 || paths[0].Kind != device.Tailnet {
		t.Fatalf("disabled LAN still offered: %+v", paths)
	}
}

func TestManagerRebalanceMovesCarrierToNewPreferredPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN)
	id := client.ID()
	// Both ends change order before either drops its carrier: a peer still preferring the LAN would
	// yield to it (see yieldToPreferred) and win the redial back onto the old path.
	for _, n := range []*testNode{a, b} {
		n.setOrder(device.Tailnet, device.LAN)
	}
	for _, n := range []*testNode{a, b} {
		n.manager.Rebalance()
	}
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
	if client.ID() != id {
		t.Fatal("session rebuilt by a path change")
	}
	// Already on the preferred path: another rebalance leaves the carrier alone.
	epoch := a.manager.Activity(b.id.ID).Epoch
	a.manager.Rebalance()
	time.Sleep(300 * time.Millisecond)
	if got := a.manager.Activity(b.id.ID); got.Path.Kind != device.Tailnet || got.Epoch != epoch {
		t.Fatalf("settled carrier was dropped: %+v", got)
	}
}

var fedUntil sync.Map

// feed gives n's directory a window of probe results for the path to remote: rtts[i] > 0 is a probe
// answered in that many milliseconds, 0 a probe that failed. The last result is always an answer so the
// path ends reachable.
func feed(t *testing.T, n, remote *testNode, kind device.PathKind, url string, rtts ...int) {
	t.Helper()
	trusted, _ := n.trust.Find(remote.id.ID)
	// Probe times only move forward, and pairTestNodes already observed each path "now".
	key := n.id.ID + url
	start := time.Now()
	if last, ok := fedUntil.Load(key); ok && last.(time.Time).After(start) {
		start = last.(time.Time).Add(time.Millisecond)
	}
	defer func() { fedUntil.Store(key, start.Add(time.Duration(len(rtts))*time.Millisecond)) }()
	for i, ms := range rtts {
		at := start.Add(time.Duration(i) * time.Millisecond)
		if ms == 0 {
			if err := n.directory.ProbeFailed(remote.id.ID, kind, url, 1, at); err != nil {
				t.Fatal(err)
			}
			continue
		}
		path := device.Path{Kind: kind, Address: url, BootID: remote.manager.BootID(), ProviderGeneration: 1, VerifiedAt: at, ExpiresAt: time.Now().Add(time.Minute), RTT: time.Duration(ms) * time.Millisecond}
		if err := n.directory.Observe(remote.id.Certificate.Leaf, trusted.Generation, path); err != nil {
			t.Fatal(err)
		}
	}
}

// feedBoth gives both ends the same view of the link, as two healthy nodes would measure it.
func feedBoth(t *testing.T, a, b *testNode, kind device.PathKind, rtts ...int) {
	t.Helper()
	urls := func(n *testNode) string {
		if kind == device.LAN {
			return n.lan.URL
		}
		return n.tailnet.URL
	}
	feed(t, a, b, kind, urls(b), rtts...)
	feed(t, b, a, kind, urls(a), rtts...)
}

func steady(ms, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = ms
	}
	return out
}

func awaitSwitch(t *testing.T, n *testNode, from, to device.PathKind, reason string) {
	t.Helper()
	select {
	case sw := <-n.switched:
		if sw.from != from || sw.to != to || sw.reason != reason {
			t.Fatalf("switch = %+v, want %s>%s (%s)", sw, from, to, reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no path switch")
	}
}

func TestSmartModeConnectsOnTheBestScoredPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	a.smart.Store(true)
	b.smart.Store(true)
	pairTestNodes(t, a, b)
	feedBoth(t, a, b, device.LAN, steady(400, 8)...)
	feedBoth(t, a, b, device.Tailnet, steady(40, 8)...)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
}

func TestSmartModeMovesALiveLinkOnlyWhenAnotherPathIsLastingBetter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	a.smart.Store(true)
	b.smart.Store(true)
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN) // nothing measured yet: the configured order
	id := client.ID()
	// Close call: Tailnet is 10 ms faster, well inside the margin, so the link must stay.
	feedBoth(t, a, b, device.LAN, steady(100, 8)...)
	feedBoth(t, a, b, device.Tailnet, steady(90, 8)...)
	time.Sleep(700 * time.Millisecond)
	if got := a.manager.Activity(b.id.ID).Path.Kind; got != device.LAN {
		t.Fatalf("moved to %s for a marginal gain", got)
	}
	select {
	case sw := <-a.switched:
		t.Fatalf("unexpected switch %+v", sw)
	default:
	}
	// Now the LAN path turns slow: Tailnet is clearly and lastingly better.
	feedBoth(t, a, b, device.LAN, steady(500, 30)...)
	awaitSwitch(t, a, device.LAN, device.Tailnet, "better")
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
	if client.ID() != id {
		t.Fatal("session rebuilt by a path switch")
	}
}

func TestSmartModeLeavesAFailingPathWithoutWaiting(t *testing.T) {
	old := testSwitching
	testSwitching.Sustain = time.Hour // only the failing-path rule can move the link in this test
	t.Cleanup(func() { testSwitching = old })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	a.smart.Store(true)
	b.smart.Store(true)
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN)
	// A random block: the LAN path answers, then drops, over and over.
	feedBoth(t, a, b, device.LAN, 20, 0, 20, 0, 20, 0, 20, 0, 20, 0, 20)
	feedBoth(t, a, b, device.Tailnet, steady(60, 10)...)
	awaitSwitch(t, a, device.LAN, device.Tailnet, "unhealthy")
	waitPath(t, a.manager, b.id.ID, device.Tailnet)
}

func TestManualModeIgnoresMeasuredQuality(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	client := openClient(t, ctx, a, b)
	defer client.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN)
	feedBoth(t, a, b, device.LAN, 20, 0, 20, 0, 20, 0, 20, 0, 20, 0, 20)
	feedBoth(t, a, b, device.Tailnet, steady(60, 10)...)
	time.Sleep(600 * time.Millisecond)
	if got := a.manager.Activity(b.id.ID).Path.Kind; got != device.LAN {
		t.Fatalf("manual mode left %s", got)
	}
}

func TestHeartbeatMeasuresActivePathAndPeersExchangeQuality(t *testing.T) {
	ctx := t.Context()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	a.setOrder(device.LAN)
	b.setOrder(device.LAN)
	// Whichever end dials, only the dialing end owns the path history. Open from a: a dials.
	session := openClient(t, ctx, a, b)
	defer session.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN)
	if got := a.manager.Activity(b.id.ID); !got.Outgoing {
		t.Fatalf("a did not dial: %+v", got)
	}
	if !a.manager.CarriesPath(b.id.ID, device.LAN, b.lan.URL) || b.manager.CarriesPath(a.id.ID, device.LAN, a.lan.URL) {
		t.Fatal("only the dialing end carries the path")
	}
	// The pairing probe left one sample; heartbeats every 100ms add to it with no probe at all.
	until := time.Now().Add(3 * time.Second)
	var q device.Quality
	for time.Now().Before(until) {
		devices, _ := a.directory.Snapshot(time.Now())
		for _, p := range devices[0].Paths {
			if p.Kind == device.LAN && p.Address == b.lan.URL {
				q = p.Quality
			}
		}
		if q.Samples >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if q.Samples < 5 || q.Loss != 0 {
		t.Fatalf("heartbeat not recorded on active path: %+v", q)
	}
	// b did not dial, so its heartbeats add nothing to its own paths.
	devices, _ := b.directory.Snapshot(time.Now())
	for _, p := range devices[0].Paths {
		if p.Quality.Samples > 1 {
			t.Fatalf("incoming carrier recorded heartbeats: %+v", p)
		}
	}
}

func TestQualitySummaryReachesPeerForDisplayOnly(t *testing.T) {
	ctx := t.Context()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	for _, n := range []*testNode{a, b} {
		n.manager.opts.QualityEvery = 20 * time.Millisecond
	}
	pairTestNodes(t, a, b)
	a.setOrder(device.LAN)
	b.setOrder(device.LAN)
	feed(t, a, b, device.LAN, b.lan.URL, steady(25, 8)...)
	session := openClient(t, ctx, a, b)
	defer session.Close()
	waitPath(t, a.manager, b.id.ID, device.LAN)
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		devices, _ := b.directory.Snapshot(time.Now())
		if got := devices[0].PeerQuality; len(got) > 0 {
			if got[0].Kind != device.LAN || got[0].Quality.Samples < 8 {
				t.Fatalf("peer quality = %+v", got)
			}
			// It is only displayed: b's own paths are untouched by what a reported.
			for _, p := range devices[0].Paths {
				if p.Kind == device.LAN && p.Quality.Samples > 1 {
					t.Fatalf("report leaked into own measurements: %+v", p)
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("b never received a's quality summary")
}
