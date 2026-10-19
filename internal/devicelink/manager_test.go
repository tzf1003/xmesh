package devicelink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	n := &testNode{id: id, trust: trust, directory: directory}
	manager, err := NewManager(t.Context(), Options{
		Identity: id, Trust: trust, Directory: directory, BootID: name + "-boot", RecoveryWindow: 2 * time.Second, Heartbeat: 100 * time.Millisecond, PingTimeout: 100 * time.Millisecond,
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
