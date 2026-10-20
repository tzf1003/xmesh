package control

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// The registered routes are exactly the contract's route table (CTR-CTL-12): every row is served
// or answers not_implemented, nothing else is reachable.
func Test_ControlRegistersEveryContractRoute(t *testing.T) {
	f := newFixture(t)
	table := v2.Routes()
	ids := map[string]bool{}
	for _, r := range table {
		ids[r.ID] = true
		if f.server.handlers[r.ID] == nil && f.server.hasCapability(r.Capability) {
			t.Errorf("%s declares capability %s but has no handler", r.ID, r.Capability)
		}
	}
	for id := range f.server.handlers {
		if !ids[id] {
			t.Errorf("handler %s is not in the route table", id)
		}
	}
	for _, r := range table {
		path := strings.NewReplacer("{name}", "x", "{client_id}", "x").Replace(r.Path)
		if f.server.hasCapability(r.Capability) {
			continue
		}
		var body any
		if r.Body != nil {
			body = map[string]any{}
		}
		apiError(t, f.do(r.Method, path, body, nil), v2.ErrNotImplemented)
	}
}

func Test_ControlStatus(t *testing.T) {
	f := newFixture(t)
	r := f.ok("GET", "/v2/status", nil, "StatusResponse")
	if r.Header.Get(v2.HeaderVersion) != v2.APIVersion || r.Header.Get("Content-Type") != v2.ContentTypeJSON {
		t.Fatalf("headers: %v", r.Header)
	}
	var st v2.StatusResponse
	decodeAs(t, r, &st)
	if st.Control.APIVersion != v2.APIVersion || st.Control.KernelVersion != "test" || len(st.Control.BootID) != 8 || st.Lifecycle != "running" || st.PID != os.Getpid() {
		t.Fatalf("%+v", st)
	}
	if st.Hostname != "ctl-node" || st.Paths.ConfigFile != f.path || st.Paths.StateDir != f.server.o.StateDir || st.Paths.AuditDir == nil || st.Drain.Draining {
		t.Fatalf("%+v", st)
	}
	want := map[v2.Capability]bool{v2.CapStatus: true, v2.CapEvents: true, v2.CapPeers: true, v2.CapSessions: true, v2.CapDrain: true, v2.CapConfigRead: true, v2.CapConfigWrite: true, v2.CapPats: true, v2.CapGateway: true, v2.CapPair: true, v2.CapIngress: true, v2.CapAudit: true}
	if len(st.Control.Capabilities) != len(want) {
		t.Fatalf("capabilities: %v", st.Control.Capabilities)
	}
	for _, c := range st.Control.Capabilities {
		if !want[c] {
			t.Fatalf("unexpected capability %s", c)
		}
	}
}

// Warnings are derived: the host's own plus peer_offline and audit_dropped from what the server saw.
func Test_ControlStatusWarnings(t *testing.T) {
	f := newFixture(t)
	f.host.mutate(func(h *fakeHost) {
		h.peers = []v2.Peer{peer("box", true), peer("nas", false)}
		h.warnings = []v2.Warning{{Code: v2.WarnRestartRequired, Severity: v2.SeverityInfo, Message: "需要重启"}}
	})
	if _, err := f.server.refreshPeers(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.server.Observe(map[string]any{"event": "audit_dropped"})
	var st v2.StatusResponse
	decodeAs(t, f.ok("GET", "/v2/status", nil, "StatusResponse"), &st)
	var codes []v2.WarningCode
	for _, w := range st.Warnings {
		codes = append(codes, w.Code)
	}
	if strings.Join([]string{string(codes[0]), string(codes[1]), string(codes[2])}, ",") != "audit_dropped,peer_offline,restart_required" || len(codes) != 3 {
		t.Fatalf("%v", codes)
	}
	if st.Peers.Total != 2 || st.Peers.Online != 1 {
		t.Fatalf("peers: %+v", st.Peers)
	}
}

func Test_ControlRoutesAndErrors(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/nope"}, {"POST", "/v2/status"}, {"GET", "/v2/drain"}, {"DELETE", "/v2/config"},
		{"GET", "/mcp"}, {"GET", "/control/drain"}, {"GET", "/v2/pats/x"},
	} {
		apiError(t, f.do(c.method, c.path, nil, nil), v2.ErrNotFound)
	}
}

func Test_ControlRejectsBrowserRequests(t *testing.T) {
	f := newFixture(t)
	for _, h := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		apiError(t, f.do("GET", "/v2/status", nil, h), v2.ErrForbidden)
	}
}

func Test_ControlRequestValidation(t *testing.T) {
	f := newFixture(t)
	// content type
	e := apiError(t, f.do("POST", "/v2/drain", map[string]string{"mode": "keep"}, map[string]string{"Content-Type": "text/plain"}), v2.ErrInvalidArgs)
	if e.Field != "Content-Type" {
		t.Fatalf("%+v", e)
	}
	// unknown field in a closed request schema
	if e = apiError(t, f.do("POST", "/v2/drain", map[string]any{"mode": "keep", "force": true}, nil), v2.ErrInvalidArgs); e.Field != "force" {
		t.Fatalf("%+v", e)
	}
	if e = apiError(t, f.do("POST", "/v2/drain", map[string]string{"mode": "bogus"}, nil), v2.ErrInvalidArgs); e.Field != "mode" {
		t.Fatalf("%+v", e)
	}
	// 64 KiB for everything but the configuration
	big := map[string]string{"mode": strings.Repeat("k", v2.MaxRequestBytes)}
	apiError(t, f.do("POST", "/v2/drain", big, nil), v2.ErrPayloadTooLarge)
}

// The device reading is optional: a peer that has one validates against the schema, and a peer without
// one has no info key at all.
func Test_ControlPeerInfoShape(t *testing.T) {
	f := newFixture(t)
	read := peer("read", true)
	at := v2.Now()
	read.Info = &v2.PeerInfo{RefreshedAt: &at}
	failed := peer("failed", true)
	why := "sys.info 暂不可用"
	failed.Info = &v2.PeerInfo{Error: &why}
	f.host.mutate(func(h *fakeHost) { h.peers = []v2.Peer{read, failed, peer("plain", true)} })
	body := string(f.ok("GET", "/v2/peers", nil, "PeersResponse").Body)
	if !strings.Contains(body, `"info":{"refreshed_at":"`) || !strings.Contains(body, `"error":"sys.info 暂不可用"`) {
		t.Fatalf("readings missing:\n%s", body)
	}
	if strings.Count(body, `"info"`) != 2 {
		t.Fatalf("a peer without a reading carries info:\n%s", body)
	}
}

func Test_ControlPeersAndSessions(t *testing.T) {
	f := newFixture(t)
	f.host.mutate(func(h *fakeHost) {
		h.peers = []v2.Peer{peer("box", true)}
		h.ptys = []v2.PtySession{pty("p-abcdef12")}
		h.execs = []v2.ExecRun{{Kind: "exec", ID: "e-1", Caller: "n1/s", Cmd: "cargo", Mode: "shared", State: "running", StartedAt: v2.Now()}}
		h.leases = []v2.BackendLease{{Kind: "lease", Resource: "backend:ida", Owner: "n1/s", Token: 3, ExpiresAt: v2.Now()}}
	})
	var peers v2.PeersResponse
	decodeAs(t, f.ok("GET", "/v2/peers", nil, "PeersResponse"), &peers)
	selves := 0
	for _, p := range peers.Peers {
		if p.Self {
			selves++
		}
	}
	if len(peers.Peers) != 2 || selves != 1 {
		t.Fatalf("%+v", peers)
	}
	var sessions v2.SessionsResponse
	decodeAs(t, f.ok("GET", "/v2/sessions", nil, "SessionsResponse"), &sessions)
	if len(sessions.PTY) != 1 || len(sessions.Exec) != 1 || len(sessions.Leases) != 1 || sessions.Exec[0].Cmd != "cargo" {
		t.Fatalf("%+v", sessions)
	}
	// empty views are arrays, never null
	f.host.mutate(func(h *fakeHost) { h.ptys, h.execs, h.leases = nil, nil, nil })
	r := f.ok("GET", "/v2/sessions", nil, "SessionsResponse")
	if strings.Contains(string(r.Body), "null") {
		t.Fatalf("%s", r.Body)
	}
}

func drainState(t *testing.T, r *Response) v2.DrainResponse {
	t.Helper()
	var out v2.DrainResponse
	decodeAs(t, r, &out)
	return out
}

// The drain rules of RUL-16.
func Test_ControlDrainResumeRules(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	f.host.mutate(func(h *fakeHost) {
		h.ptys = []v2.PtySession{pty("p-abcdef12"), pty("p-abcdef13")}
		h.leases = []v2.BackendLease{{Kind: "lease", Resource: "backend:ida", Owner: "n1/s", Token: 3, ExpiresAt: v2.Now()}}
	})
	// resume while not draining is a no-op
	if d := drainState(t, f.ok("POST", "/v2/resume", nil, "DrainResponse")); d.Drain.Draining {
		t.Fatalf("%+v", d)
	}
	// empty body means keep
	first := drainState(t, f.ok("POST", "/v2/drain", nil, "DrainResponse"))
	if !first.Drain.Draining || *first.Drain.Mode != v2.DrainKeep || first.Drain.Since == nil || first.ClosedPTY != 0 {
		t.Fatalf("%+v", first)
	}
	events.next(t, func(e v2.Event) bool {
		var d v2.LifecycleEventData
		return e.Type == v2.EventLifecycle && func() bool { payload(t, e, &d); return d.Change == "draining" }()
	})
	// repeating keep is a no-op and keeps since
	again := drainState(t, f.ok("POST", "/v2/drain", map[string]string{"mode": "keep"}, "DrainResponse"))
	if again.Drain.Since.String() != first.Drain.Since.String() || len(f.host.kills) != 1 {
		t.Fatalf("%+v kills=%v", again, f.host.kills)
	}
	// keep -> kill is allowed, ends the resources, and does not reset since
	kill := drainState(t, f.ok("POST", "/v2/drain", map[string]string{"mode": "kill"}, "DrainResponse"))
	if *kill.Drain.Mode != v2.DrainKill || kill.ClosedPTY != 2 || kill.ReleasedLeases != 1 || kill.Drain.Since.String() != first.Drain.Since.String() {
		t.Fatalf("%+v", kill)
	}
	// kill -> keep is a conflict
	if e := apiError(t, f.do("POST", "/v2/drain", map[string]string{"mode": "keep"}, nil), v2.ErrConflict); e.Reason != "drain_downgrade" {
		t.Fatalf("%+v", e)
	}
	var st v2.StatusResponse
	decodeAs(t, f.ok("GET", "/v2/status", nil, "StatusResponse"), &st)
	if !st.Drain.Draining || *st.Drain.Mode != v2.DrainKill {
		t.Fatalf("%+v", st.Drain)
	}
	if d := drainState(t, f.ok("POST", "/v2/resume", nil, "DrainResponse")); d.Drain.Draining {
		t.Fatalf("%+v", d)
	}
	events.next(t, func(e v2.Event) bool {
		var d v2.LifecycleEventData
		return e.Type == v2.EventLifecycle && func() bool { payload(t, e, &d); return d.Change == "resumed" }()
	})
}

// A drain set elsewhere (the mesh drain tool) still shows up with a mode and a start time.
func Test_ControlDrainSetElsewhere(t *testing.T) {
	f := newFixture(t)
	f.host.mutate(func(h *fakeHost) { h.draining = true })
	var st v2.StatusResponse
	decodeAs(t, f.ok("GET", "/v2/status", nil, "StatusResponse"), &st)
	if !st.Drain.Draining || *st.Drain.Mode != v2.DrainKeep || st.Drain.Since == nil {
		t.Fatalf("%+v", st.Drain)
	}
	f.host.mutate(func(h *fakeHost) { h.draining = false })
	decodeAs(t, f.ok("GET", "/v2/status", nil, "StatusResponse"), &st)
	if st.Drain.Draining || st.Drain.Mode != nil {
		t.Fatalf("%+v", st.Drain)
	}
}

// conformance: CTR-CTL-01 the endpoint file names the transport, address, boot id and capabilities
func Test_ControlEndpointFile(t *testing.T) {
	f := newFixture(t)
	raw, err := os.ReadFile(f.server.o.StateDir + "/" + FileName)
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "ControlEndpointFile", raw)
	ep, err := ReadEndpoint(f.server.o.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Node != "ctl-node" || ep.PID != os.Getpid() || ep.APIVersion != v2.APIVersion || ep.KernelVersion != "test" || ep.Address() != f.server.Endpoint().Address() || ep.BootID != f.server.Bus().BootID() {
		t.Fatalf("%+v", ep)
	}
	if st, err := os.Stat(f.server.o.StateDir + "/" + FileName); err != nil || (os.PathSeparator == '/' && st.Mode().Perm() != 0600) {
		t.Fatalf("control.json mode: %v %v", st, err)
	}
	f.server.Close()
	if _, err = ReadEndpoint(f.server.o.StateDir); err == nil {
		t.Fatal("control.json should be removed on close")
	}
	if _, err = NewClient(ep).Do(t.Context(), "GET", "/v2/status", nil, nil); err == nil {
		t.Fatal("channel still answering after close")
	}
}

// conformance: CTR-CTL-01 a client finds a node's channel from its state directory or by enumerating the per-user directories
func Test_ControlDiscover(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "AppData", "ProgramData"} {
		t.Setenv(name, root)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip(err)
	}
	for _, node := range []string{"b-node", "a-node"} {
		dir := base + "/xmesh/" + node
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = WriteEndpoint(dir, v2.ControlEndpointFile{APIVersion: v2.APIVersion, Node: node, Transport: v2.TransportUnix, Endpoint: "/x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(base+"/xmesh/no-endpoint", 0700); err != nil {
		t.Fatal(err)
	}
	found, err := Discover()
	if err != nil || len(found) != 2 || found[0].Node != "a-node" || !strings.HasSuffix(found[1].Dir, "b-node") {
		t.Fatalf("%v %+v", err, found)
	}
}

func Test_SystemStateRoot(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := SystemStateRootFor("linux", env(map[string]string{"ProgramData": "/x"})); got != "" {
		t.Fatalf("non-Windows has no system root: %q", got)
	}
	if got := SystemStateRootFor("windows", env(map[string]string{"ProgramData": `D:\PD\`})); got != `D:\PD\xmesh` {
		t.Fatal(got)
	}
	if got := SystemStateRootFor("windows", env(nil)); got != `C:\ProgramData\xmesh` {
		t.Fatal(got)
	}
}

// A SYSTEM service's node, under %ProgramData%\xmesh, is found next to the user's own nodes.
func Test_ControlDiscoverSystemServices(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("%ProgramData% is Windows only")
	}
	root := t.TempDir()
	for _, name := range []string{"AppData", "ProgramData"} {
		t.Setenv(name, root)
	}
	dir := filepath.Join(root, "xmesh", "svc-node")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteEndpoint(dir, v2.ControlEndpointFile{APIVersion: v2.APIVersion, Node: "svc-node", Transport: v2.TransportPipe, Endpoint: `\\.\pipe\x`}); err != nil {
		t.Fatal(err)
	}
	found, err := Discover()
	if err != nil || len(found) == 0 || found[0].Node != "svc-node" {
		t.Fatalf("%v %+v", err, found)
	}
}

func Test_WaitTimeout(t *testing.T) {
	var done sync.WaitGroup
	if !waitTimeout(&done, time.Second) {
		t.Fatal("an empty WaitGroup should be finished")
	}
	done.Add(1)
	if waitTimeout(&done, 20*time.Millisecond) {
		t.Fatal("a pending WaitGroup reported finished")
	}
	done.Done()
	if !waitTimeout(&done, time.Second) {
		t.Fatal("a released WaitGroup should be finished")
	}
}

// acceptStuckListener never returns from Accept, like a Serve loop that cannot be stopped.
type acceptStuckListener struct {
	accepting chan struct{}
	block     chan struct{}
}

func (l *acceptStuckListener) Accept() (net.Conn, error) {
	l.accepting <- struct{}{}
	<-l.block
	return nil, net.ErrClosed
}

func (l *acceptStuckListener) Close() error   { return nil }
func (l *acceptStuckListener) Addr() net.Addr { return &net.TCPAddr{} }

// stopHTTP must come back when ctx ends, even though Serve never returns.
func Test_StopHTTPReturnsWhenServeNeverStops(t *testing.T) {
	l := &acceptStuckListener{accepting: make(chan struct{}, 1), block: make(chan struct{})}
	defer close(l.block)
	srv := &http.Server{}
	go func() { _ = srv.Serve(l) }()
	<-l.accepting
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		stopHTTP(ctx, srv)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopHTTP waited for a Serve loop that never returns")
	}
}

// Once the server is stopping, a request that still reaches it is refused as unavailable.
func Test_ControlRefusesRequestsOnceStopping(t *testing.T) {
	f := newFixture(t)
	f.server.Close()
	rec := httptest.NewRecorder()
	f.server.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d after Close, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
