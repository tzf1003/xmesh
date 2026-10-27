package devicelink

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/device"
)

// callTool runs the test tool through a session and fails the test if it does not answer.
func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "count.once", Arguments: map[string]any{}})
	if err == nil && out.IsError {
		err = errors.New("tool failed")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// openSession opens a session from one node to another and returns the client with its logical session id.
func openSession(t *testing.T, ctx context.Context, from, to *testNode) (*mcp.ClientSession, string) {
	t.Helper()
	transport, err := from.manager.Open(ctx, to.id.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "upstream", Version: "2"}, nil)
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, transport.Session.id
}

// attachedOutgoing reports whether a session runs on a carrier its own end dialed, and whether it runs on any.
func attachedOutgoing(m *Manager, device, session string) (outgoing, attached bool) {
	p, err := m.peer(device)
	if err != nil {
		return false, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.sessions[session]
	if l == nil || l.attached == nil {
		return false, false
	}
	return l.attached.outgoing, true
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(4 * time.Second); !condition(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// closeLink drops the carrier of one direction the way a broken network would.
func closeLink(t *testing.T, m *Manager, id string, outgoing bool) {
	t.Helper()
	p, _ := m.peer(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	c, _ := p.slotLocked(outgoing)
	if c == nil {
		t.Fatal("no carrier to drop")
	}
	c.cancel()
	_ = c.conn.CloseNow()
}

// bothDirections opens a session each way and waits until each end has a carrier of its own.
func bothDirections(t *testing.T, ctx context.Context, a, b *testNode) (ab, ba *mcp.ClientSession, abID, baID string) {
	t.Helper()
	ab, abID = openSession(t, ctx, a, b)
	ba, baID = openSession(t, ctx, b, a)
	waitLinks(t, a.manager, b.id.ID, 2)
	waitLinks(t, b.manager, a.id.ID, 2)
	return ab, ba, abID, baID
}

func TestSessionBorrowsThePeersCarrierThenMovesToItsOwn(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	first, _ := openSession(t, ctx, a, b)
	waitLinks(t, a.manager, b.id.ID, 1)
	// b has only the carrier a dialed, so its call runs on that one while its own is dialed.
	second, id := openSession(t, ctx, b, a)
	callTool(t, ctx, second)
	waitLinks(t, b.manager, a.id.ID, 2)
	waitFor(t, "session to move to its own end's carrier", func() bool {
		outgoing, attached := attachedOutgoing(b.manager, a.id.ID, id)
		return attached && outgoing
	})
	callTool(t, ctx, first)
	callTool(t, ctx, second)
	if a.accepts.Load() != 1 || b.accepts.Load() != 1 {
		t.Fatal("a session was rebuilt", a.accepts.Load(), b.accepts.Load())
	}
}

func TestOneSlotKeepsServingWhenTheOtherDrops(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	ab, ba, abID, baID := bothDirections(t, ctx, a, b)
	waitFor(t, "both sessions on their own carriers", func() bool {
		x, ok1 := attachedOutgoing(a.manager, b.id.ID, abID)
		y, ok2 := attachedOutgoing(b.manager, a.id.ID, baID)
		return ok1 && ok2 && x && y
	})
	closeLink(t, a.manager, b.id.ID, true)
	// Never offline: b's carrier is still there, and the session moves onto it.
	if got := a.manager.Activity(b.id.ID); got.State != "online" && got.State != "unstable" {
		t.Fatalf("lost the device with one carrier left: %+v", got)
	}
	callTool(t, ctx, ab)
	callTool(t, ctx, ba)
	// The lost slot is rebuilt because a still has calls to make, with the same sessions.
	waitLinks(t, a.manager, b.id.ID, 2)
	// The surviving carrier alone outlives the recovery window, which only counts a device with none.
	time.Sleep(2500 * time.Millisecond)
	callTool(t, ctx, ab)
	callTool(t, ctx, ba)
	if a.accepts.Load() != 1 || b.accepts.Load() != 1 {
		t.Fatal("a session was rebuilt", a.accepts.Load(), b.accepts.Load())
	}
}

func TestProviderChangeRebuildsBothSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	ab, ba, _, _ := bothDirections(t, ctx, a, b)
	for _, n := range []*testNode{a, b} {
		if err := n.directory.SetProvider(device.LAN, 2, false); err != nil {
			t.Fatal(err)
		}
		n.manager.DisablePath(device.LAN)
	}
	for _, n := range []*testNode{a, b} {
		peer := map[*testNode]*testNode{a: b, b: a}[n]
		waitFor(t, "Tailnet carriers", func() bool {
			links := n.manager.Links(peer.id.ID)
			return len(links) == 2 && links[0].Path.Kind == device.Tailnet && links[1].Path.Kind == device.Tailnet
		})
	}
	callTool(t, ctx, ab)
	callTool(t, ctx, ba)
}

func TestIdleSecondCarrierClosesAndTheFirstStays(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	idle := func(o *Options) { o.IdleClose = 400 * time.Millisecond }
	a, b := newTestNode(t, "a", idle), newTestNode(t, "b", idle)
	pairTestNodes(t, a, b)
	ab, ba, _, _ := bothDirections(t, ctx, a, b)
	// With nothing moving, exactly one carrier is closed, by its dialer, and nothing brings it back.
	for _, n := range []*testNode{a, b} {
		peer := map[*testNode]*testNode{a: b, b: a}[n]
		waitLinks(t, n.manager, peer.id.ID, 1)
	}
	time.Sleep(700 * time.Millisecond)
	for _, n := range []*testNode{a, b} {
		peer := map[*testNode]*testNode{a: b, b: a}[n]
		if got := len(n.manager.Links(peer.id.ID)); got != 1 {
			t.Fatalf("idle carriers = %d, want the one that stays", got)
		}
	}
	// Both sessions still work over it.
	callTool(t, ctx, ab)
	callTool(t, ctx, ba)
	if a.accepts.Load() != 1 || b.accepts.Load() != 1 {
		t.Fatal("a session was rebuilt", a.accepts.Load(), b.accepts.Load())
	}
}

func TestHandoffKeepsACallRunningUntilTheNewCarrierTakesOver(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	b.started, b.release = make(chan struct{}), make(chan struct{})
	pairTestNodes(t, a, b)
	session, _ := openSession(t, ctx, a, b)
	waitPath(t, a.manager, b.id.ID, device.LAN)
	result := make(chan error, 1)
	go func() {
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "count.once", Arguments: map[string]any{}})
		if err == nil && out.IsError {
			err = errors.New("tool failed")
		}
		result <- err
	}()
	<-b.started
	p, _ := a.manager.peer(b.id.ID)
	p.mu.Lock()
	old, _ := p.slotLocked(true)
	p.handoff = &handoff{from: old, to: device.Tailnet, reason: "better"}
	p.signalLocked()
	p.mu.Unlock()
	waitFor(t, "Tailnet to take over", func() bool {
		links := a.manager.Links(b.id.ID)
		return len(links) == 1 && links[0].Path.Kind == device.Tailnet
	})
	close(b.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case sw := <-a.switched:
		if sw.from != device.LAN || sw.to != device.Tailnet || sw.reason != "better" {
			t.Fatalf("switch = %+v", sw)
		}
	case <-time.After(time.Second):
		t.Fatal("handoff was not reported")
	}
	if b.calls.Load() != 1 || b.accepts.Load() != 1 {
		t.Fatal("call repeated or session rebuilt", b.calls.Load(), b.accepts.Load())
	}
}

// gate forwards TCP to a target and, once frozen, swallows everything in both directions: the link
// stays open and silent, as a path does when a middlebox starts dropping packets.
type gate struct {
	ln     net.Listener
	frozen atomic.Bool
}

func newGate(t *testing.T, target string) *gate {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				_ = in.Close()
				continue
			}
			t.Cleanup(func() { _ = in.Close(); _ = out.Close() })
			pump := func(dst, src net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if n > 0 && !g.frozen.Load() {
						if _, werr := dst.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						if errors.Is(err, io.EOF) {
							_ = dst.Close()
						}
						return
					}
				}
			}
			go pump(out, in)
			go pump(in, out)
		}
	}()
	return g
}

func TestSilentCarrierIsReplacedBeforeItIsGivenUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	slow := func(o *Options) { o.DeadAfter = 6 * time.Second }
	a, b := newTestNode(t, "a", slow), newTestNode(t, "b", slow)
	pairTestNodes(t, a, b)
	// a reaches b's LAN listener through a gate it can freeze; the Tailnet path is direct.
	g := newGate(t, b.lan.Listener.Addr().String())
	trusted, _ := a.trust.Find(b.id.ID)
	now := time.Now()
	if err := a.directory.Lost(b.id.ID, device.LAN, b.lan.URL, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.directory.Observe(b.id.Certificate.Leaf, trusted.Generation, device.Path{Kind: device.LAN, Address: "https://" + g.ln.Addr().String(), BootID: b.manager.BootID(), ProviderGeneration: 1, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	session, id := openSession(t, ctx, a, b)
	waitPath(t, a.manager, b.id.ID, device.LAN)
	callTool(t, ctx, session)
	froze := time.Now()
	g.frozen.Store(true)
	select {
	case sw := <-a.switched:
		if sw.from != device.LAN || sw.to != device.Tailnet || sw.reason != "unresponsive" {
			t.Fatalf("switch = %+v", sw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent carrier was not replaced")
	}
	if took := time.Since(froze); took >= 4*time.Second {
		t.Fatalf("replacement waited for the silent carrier to die: %s", took)
	}
	callTool(t, ctx, session)
	waitFor(t, "silent carrier to be retired", func() bool {
		links := a.manager.Links(b.id.ID)
		return len(links) == 1 && links[0].Path.Kind == device.Tailnet
	})
	if outgoing, attached := attachedOutgoing(a.manager, b.id.ID, id); !attached || !outgoing {
		t.Fatal("session not on the replacement carrier")
	}
	if b.accepts.Load() != 1 {
		t.Fatal("session rebuilt", b.accepts.Load())
	}
}

func TestAdmissionPerSlot(t *testing.T) {
	carrierIn := func(outgoing, committed bool) *carrier { return &carrier{outgoing: outgoing, committed: committed} }
	cases := []struct {
		name     string
		cars     []*carrier
		outgoing bool
		replace  int
		dual     bool
		want     error
	}{
		{"first carrier", nil, true, -1, true, nil},
		{"other direction is independent", []*carrier{carrierIn(false, true)}, true, -1, true, nil},
		{"handshake in progress blocks its slot", []*carrier{carrierIn(true, false)}, true, -1, true, ErrUnavailable},
		{"handshake in the other slot does not", []*carrier{carrierIn(false, false)}, true, -1, true, nil},
		{"a dial does not displace a live carrier", []*carrier{carrierIn(true, true)}, true, -1, true, ErrUnavailable},
		{"unless it replaces that one", []*carrier{carrierIn(true, true)}, true, 0, true, nil},
		{"a dialed end takes the newer carrier", []*carrier{carrierIn(false, true)}, false, -1, true, nil},
		{"a peer without the capability keeps one", []*carrier{carrierIn(false, true)}, true, -1, false, ErrUnavailable},
		{"and gets its first", nil, false, -1, false, nil},
	}
	for _, c := range cases {
		p := &peerLink{cars: c.cars}
		var replace *carrier
		if c.replace >= 0 {
			replace = c.cars[c.replace]
		}
		if got := p.admitLocked(c.outgoing, replace, c.dual); !errors.Is(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if p.legacy == c.dual {
			t.Errorf("%s: legacy = %v for dual = %v", c.name, p.legacy, c.dual)
		}
	}
}

func TestSessionChoosesItsCarrier(t *testing.T) {
	m := &Manager{opts: Options{MigrateIdle: time.Second}}
	now := time.Now()
	out := &carrier{outgoing: true, committed: true}
	in := &carrier{committed: true}
	quiet := func(own bool) *logical {
		l := &logical{incoming: !own, session: NewSession("s", nil), active: now.Add(-time.Minute)}
		return l
	}
	busy := func(own bool) *logical {
		l := quiet(own)
		l.active = now
		return l
	}
	cases := []struct {
		name string
		cars []*carrier
		l    *logical
		cur  *carrier
		want *carrier
	}{
		{"own slot first", []*carrier{out, in}, quiet(true), nil, out},
		{"the peer's session belongs to the carrier the peer dialed", []*carrier{out, in}, quiet(false), nil, in},
		{"borrow when own is missing", []*carrier{in}, quiet(true), nil, in},
		{"stay on a borrowed carrier while busy", []*carrier{out, in}, busy(true), in, in},
		{"return home when quiet", []*carrier{out, in}, quiet(true), in, out},
		{"leave a silent carrier at once", []*carrier{out, in}, busy(true), out, out},
		{"nothing healthy: keep what it has", []*carrier{out, in}, busy(true), in, in},
	}
	// The last two cases need a suspect carrier; set them up explicitly.
	for i, c := range cases {
		o, n := &carrier{outgoing: true, committed: true}, &carrier{committed: true}
		remap := func(x *carrier) *carrier {
			switch x {
			case out:
				return o
			case in:
				return n
			}
			return nil
		}
		p := &peerLink{}
		for _, x := range c.cars {
			p.cars = append(p.cars, remap(x))
		}
		c.l.attached = remap(c.cur)
		want := remap(c.want)
		switch c.name {
		case "leave a silent carrier at once":
			o.suspect.Store(true)
			want = n
		case "nothing healthy: keep what it has":
			o.suspect.Store(true)
			n.suspect.Store(true)
		}
		if got := m.desiredLocked(p, c.l, now); got != want {
			t.Errorf("case %d (%s): wrong carrier", i, c.name)
		}
	}
	// A carrier being replaced is not a place to stay.
	old, fresh := &carrier{outgoing: true, committed: true, draining: true}, &carrier{outgoing: true, committed: true}
	p := &peerLink{cars: []*carrier{old, fresh}}
	l := busy(true)
	l.attached = old
	if got := m.desiredLocked(p, l, now); got != fresh {
		t.Error("session stayed on a draining carrier")
	}
}
