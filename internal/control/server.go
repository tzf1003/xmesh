package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
)

type Options struct {
	Computer *computer.Manager
	Host     Host
	Store    *config.Store
	Version  string
	// StateDir receives control.json; AuditDir is read by GET /v2/audit.
	StateDir, AuditDir string
	Listen             ListenOptions
	// Audit receives control_call records for non-GET requests.
	Audit *audit.Logger
	// Poll is the state sampling interval behind the event stream. Default 1s.
	Poll time.Duration
	// Heartbeat is the idle interval of the SSE comment line. Default 15s.
	Heartbeat time.Duration
	// Ingress serves the ingress routes; nil on a node that does not run the ingress.
	Ingress IngressAdmin
	// BackendEnv keeps the environment variables of stdio backends; nil answers the env routes
	// with unavailable. ApplyBackendEnv, when set, makes the running backends pick up a change at once.
	BackendEnv      *backendenv.Store
	ApplyBackendEnv func()
	// IngressNow, when set, replaces Ingress: it returns the ingress running now, nil while none
	// runs, for a node whose ingress comes and goes with the configuration.
	IngressNow func() IngressAdmin
	// Pairing serves the connection-password routes; nil unless the node runs the LAN network.
	Pairing        PairingAdmin
	DeviceIdentity *device.Identity
	DeviceTrust    *device.TrustStore
	// Applier reports on the roles the node starts, stops and moves when the configuration changes;
	// nil on a node that has none.
	Applier Applier
	// CI supervises the GitHub Actions runners; nil on a node that cannot host them.
	CI CIAdmin
	// CIAccounts keeps GitHub accounts and talks to GitHub with them; nil answers the account
	// routes with unavailable.
	CIAccounts CIAccounts
	// CIVisibility replaces the public-repository check; nil asks GitHub.
	CIVisibility CIVisibility
}

// Applier is the part of the node that puts a new configuration generation into effect in the
// running process, such as moving the gateway to a new listen address.
type Applier interface {
	// Settle waits until every generation adopted so far has been applied and returns what could
	// not be. With retry it first tries again what failed last time.
	Settle(ctx context.Context, retry bool) []v2.ApplyError
}

func (o Options) heartbeat() time.Duration {
	if o.Heartbeat > 0 {
		return o.Heartbeat
	}
	return v2.HeartbeatSeconds * time.Second
}

// call is what a handler reports back for the audit record of its request.
type call struct {
	route v2.Route
	// keys are the top-level keys of the request body; size its length in bytes.
	keys []string
	size int
}

type handler func(w http.ResponseWriter, r *http.Request, c *call)

type Server struct {
	o        Options
	bound    Bound
	endpoint v2.ControlEndpointFile
	srv      *http.Server
	bus      *Bus
	started  v2.Time
	caps     []v2.Capability
	routes   []route
	handlers map[string]handler
	stop     chan struct{}
	done     sync.WaitGroup
	once     sync.Once

	lifecycle    atomic.Value // string
	auditDropped atomic.Bool

	configMu   sync.Mutex // serialises PUT /v2/config and guards the fields below
	digest     string
	lastParsed map[string]any
	failures   uint64

	drainMu sync.Mutex
	drain   v2.DrainState

	peerMu           sync.Mutex
	peers            []v2.Peer
	computerEndpoint string
}

// Start opens the channel, writes control.json and begins serving. A node
// without a UI keeps working if this fails, so callers should log and continue.
func Start(o Options) (*Server, error) {
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	o.Listen.StateDir, o.Listen.Node = o.StateDir, o.Store.Get().Node.Name
	listener, bound, err := Listen(o.Listen)
	if err != nil {
		return nil, err
	}
	s := &Server{o: o, bound: bound, bus: NewBus(), started: v2.Now(), stop: make(chan struct{})}
	s.computerEndpoint = o.Store.Get().Agent.Computer.Endpoint
	if s.computerEndpoint == "" {
		s.computerEndpoint = computer.DefaultEndpoint()
	}
	s.lifecycle.Store("starting")
	s.caps = capabilities(o)
	s.routes = compileRoutes(v2.Routes())
	s.handlers = s.table()
	s.digest = s.fileDigest()
	if path := o.Store.Path(); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			s.lastParsed = parsedOf(raw)
		}
	}
	_, s.failures = o.Store.State()
	s.endpoint = v2.ControlEndpointFile{
		APIVersion: v2.APIVersion, KernelVersion: o.Version, Node: o.Listen.Node, PID: os.Getpid(), BootID: s.bus.BootID(),
		Transport: bound.Transport, Endpoint: bound.Address, StartedAt: s.started, Capabilities: s.caps,
	}
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	if err = WriteEndpoint(o.StateDir, s.endpoint, o.Listen.AllowSIDs...); err != nil {
		_ = listener.Close()
		return nil, err
	}
	// The baseline is the running state, so the first sample does not announce "starting" ->
	// "running" a second time; the lifecycle event below is what tells subscribers.
	s.lifecycle.Store("running")
	base := s.baseline()
	s.done.Add(2)
	go func() {
		defer s.done.Done()
		_ = s.srv.Serve(listener)
	}()
	go func() {
		defer s.done.Done()
		s.poll(base)
	}()
	s.lifecycleEvent("running")
	return s, nil
}

// capabilities lists what this kernel implements. Routes of the others answer not_implemented.
func capabilities(o Options) []v2.Capability {
	caps := []v2.Capability{v2.CapStatus, v2.CapEvents, v2.CapPeers, v2.CapSessions, v2.CapDrain}
	if o.Store.Path() != "" {
		caps = append(caps, v2.CapConfigRead, v2.CapConfigWrite, v2.CapPats, v2.CapGateway)
	}
	caps = append(caps, v2.CapPair, v2.CapIngress, v2.CapAudit)
	if o.CI != nil && o.Store.Path() != "" {
		caps = append(caps, v2.CapCI)
	}
	if o.Computer != nil {
		caps = append(caps, v2.CapComputer)
	}
	return caps
}

func (s *Server) hasCapability(c v2.Capability) bool {
	for _, have := range s.caps {
		if have == c {
			return true
		}
	}
	return false
}

// Endpoint is the control.json this server wrote.
func (s *Server) Endpoint() Endpoint {
	return Endpoint{ControlEndpointFile: s.endpoint, Dir: s.o.StateDir}
}
func (s *Server) Bus() *Bus { return s.bus }

// Close ends the stream, closes the listener and removes control.json.
func (s *Server) Close() {
	s.once.Do(func() {
		s.lifecycle.Store("stopping")
		s.lifecycleEvent("stopping")
		close(s.stop)
		s.bus.Close()
		// Streams end by themselves once the bus is closed; give them time to flush.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopHTTP(ctx, s.srv)
		// A listener that refuses to stop leaves Serve blocked in Accept; shutdown must not
		// wait for it forever. While Serve may still hold the pipe, the endpoint stays, so
		// discovery keeps pointing at a server that answers "stopping". The endpoint goes once
		// Serve has really returned, however late that is.
		if !waitTimeout(&s.done, serveStopTimeout) {
			go func() {
				s.done.Wait()
				RemoveEndpoint(s.o.StateDir, s.endpoint)
			}()
			return
		}
		RemoveEndpoint(s.o.StateDir, s.endpoint)
	})
}

// Observe turns an audit record into events and status. It is registered with the audit logger
// and must not block.
func (s *Server) Observe(record map[string]any) {
	switch record["event"] {
	case "audit_dropped":
		s.auditDropped.Store(true)
		return
	case "tool_call", "pty_open", "pty_close", "pty_transfer", "pty_signal", "pty_write":
	default:
		return
	}
	text := func(key string) string { v, _ := record[key].(string); return v }
	data := v2.CallEventData{Tool: text("tool"), Device: text("device"), Caller: text("caller"), Status: text("status")}
	if client := text("oauth_client"); client != "" {
		data.OAuthClient = &client
	}
	if code := text("error"); code != "" {
		data.Error = &code
	}
	switch n := record["duration_ms"].(type) {
	case int64:
		data.DurationMS = n
	case int:
		data.DurationMS = int64(n)
	case float64:
		data.DurationMS = int64(n)
	}
	if data.Tool == "" || data.Device == "" || data.Caller == "" || (data.Status != "ok" && data.Status != "error") {
		return
	}
	_ = s.bus.Publish(v2.EventCall, data)
}

func (s *Server) lifecycleEvent(change string) {
	_ = s.bus.Publish(v2.EventLifecycle, v2.LifecycleEventData{Change: change, Drain: s.currentDrain()})
}

// route is one row of the route table with its path split for matching.
type route struct {
	v2.Route
	parts []string
}

func compileRoutes(table []v2.Route) []route {
	out := make([]route, len(table))
	for i, r := range table {
		out[i] = route{Route: r, parts: strings.Split(strings.Trim(r.Path, "/"), "/")}
	}
	return out
}
func (rt route) match(method, path string) bool {
	if rt.Method != method {
		return false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != len(rt.parts) {
		return false
	}
	for i, want := range rt.parts {
		if strings.HasPrefix(want, "{") {
			if parts[i] == "" {
				return false
			}
		} else if want != parts[i] {
			return false
		}
	}
	return true
}

// table maps route ids to handlers. Every route of the contract is registered; a route whose
// capability the kernel does not declare answers not_implemented without reaching its handler.
func (s *Server) table() map[string]handler {
	return map[string]handler{
		"control.computer.get":    s.getComputer,
		"control.computer.pause":  s.pauseComputer,
		"control.computer.revoke": s.revokeComputer,
		"control.status.get":      s.status,
		"control.events.stream":   func(w http.ResponseWriter, r *http.Request, _ *call) { s.events(w, r) },
		"control.peers.list":      s.listPeers,
		"control.sessions.list":   s.sessions,
		"control.drain.start":     s.startDrain,
		"control.drain.resume":    s.resume,
		"control.config.get":      s.getConfig,
		"control.config.put":      s.putConfig,
		"control.config.reload":   s.reloadConfig,
		"control.config.patch":    s.patchConfig,
		"control.audit.list":      s.auditList,

		"control.pair.export":            s.pairExport,
		"control.pair.import":            s.pairImport,
		"control.pair.password.get":      s.getPairPassword,
		"control.pair.password.put":      s.putPairPassword,
		"control.pair.password.delete":   s.deletePairPassword,
		"control.pair.discovered":        s.listPairDiscovered,
		"control.pair.connect":           s.connectPair,
		"control.pair.trust.delete":      s.deleteDeviceTrust,
		"control.pats.list":              s.listPats,
		"control.pats.create":            s.createPat,
		"control.pats.rotate":            s.rotatePat,
		"control.pats.delete":            s.deletePat,
		"control.backends.env.patch":     s.patchBackendEnv,
		"control.backends.env.delete":    s.deleteBackendEnv,
		"control.gateway.update":         s.updateGateway,
		"control.ingress.clients.list":   s.listIngressClients,
		"control.ingress.clients.delete": s.deleteIngressClient,
		"control.ingress.password.put":   s.putIngressPassword,

		"control.ci.get":              s.getCI,
		"control.ci.accounts.create":  s.createCIAccount,
		"control.ci.accounts.delete":  s.deleteCIAccount,
		"control.ci.accounts.repos":   s.ciReposOfAccount,
		"control.ci.bindings.create":  s.createCIBinding,
		"control.ci.bindings.patch":   s.patchCIBinding,
		"control.ci.bindings.delete":  s.deleteCIBinding,
		"control.ci.bindings.restart": s.restartCIBinding,
		"control.ci.bindings.repair":  s.repairCIBinding,
		"control.ci.import.inspect":   s.inspectCIImport,
	}
}

// recorder remembers the status and error code of a response for the audit record.
type recorder struct {
	http.ResponseWriter
	status int
	code   v2.ErrorCode
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.ResponseWriter.Write(b)
}
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// failWith writes e and lets the recorder see its code.
func failWith(w http.ResponseWriter, e *v2.Error) {
	if rec, ok := w.(*recorder); ok {
		rec.code = e.Code
	}
	fail(w, e)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	rec := &recorder{ResponseWriter: w}
	w = rec
	w.Header().Set(v2.HeaderVersion, v2.APIVersion)
	w.Header().Set("Cache-Control", "no-store")
	// A listener that outlives a stuck shutdown can still hand over connections. Once the server
	// is stopping, none of them gets new work.
	select {
	case <-s.stop:
		failWith(w, newError(v2.ErrUnavailable, "内核正在停止,不再接受请求"))
		return
	default:
	}
	// Browsers cannot reach a socket or pipe; a request that looks like one is wrong.
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		failWith(w, newError(v2.ErrForbidden, "控制通道不接受来自网页的请求"))
		return
	}
	var matched *route
	for i := range s.routes {
		if s.routes[i].match(r.Method, r.URL.Path) {
			matched = &s.routes[i]
			break
		}
	}
	if matched == nil {
		failWith(w, newError(v2.ErrNotFound, "没有这个控制接口"))
		return
	}
	h := s.handlers[matched.ID]
	if h == nil || !s.hasCapability(matched.Capability) {
		failWith(w, newError(v2.ErrNotImplemented, "此内核尚未实现该控制接口"))
		if matched.Method != "GET" {
			s.auditCall(&call{route: matched.Route}, rec, time.Now())
		}
		return
	}
	limit := int64(v2.MaxRequestBytes)
	if matched.ID == "control.config.put" || matched.ID == "control.config.patch" {
		limit = v2.MaxConfigBytes
	}
	c := &call{route: matched.Route}
	started := time.Now()
	if matched.Method != "GET" {
		// Buffer the body once so the audit record can state its size and keys.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				failWith(w, newError(v2.ErrPayloadTooLarge, "请求体过大"))
			} else {
				failWith(w, argError("body", "无法读取请求体"))
			}
			s.auditCall(c, rec, started)
			return
		}
		if len(body) > 0 {
			if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
				failWith(w, argError("Content-Type", "请求体必须是 application/json"))
				s.auditCall(c, rec, started)
				return
			}
		}
		c.size = len(body)
		var keys map[string]json.RawMessage
		if json.Unmarshal(body, &keys) == nil {
			for k := range keys {
				c.keys = append(c.keys, k)
			}
			sort.Strings(c.keys)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h(w, r, c)
		s.auditCall(c, rec, started)
		return
	}
	h(w, r, c)
}

// decode reads the JSON request body into v, refusing unknown fields. An empty body leaves v alone
// unless required.
func decode(r *http.Request, v any, required bool) *v2.Error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return argError("body", "无法读取请求体")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if required {
			return argError("body", "缺少请求体")
		}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(v); err != nil {
		field := "body"
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &typeErr) && typeErr.Field != "":
			field = typeErr.Field
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field = strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		}
		return argError(field, "请求体不符合接口定义")
	}
	if dec.More() {
		return argError("body", "请求体只能包含一个 JSON 值")
	}
	return nil
}

// auditCall records a request that changes or tries to change state (CTR-CTL-02). Only key names
// and sizes go in, never values (CTR-AUD-01, INV-10). Reads are not recorded.
func (s *Server) auditCall(c *call, rec *recorder, started time.Time) {
	if s.o.Audit == nil {
		return
	}
	status, code := "ok", any(nil)
	if rec.status >= 400 || rec.code != "" {
		status = "error"
		code = string(rec.code)
		if rec.code == "" {
			code = string(v2.ErrInternal)
		}
	}
	keys := c.keys
	if keys == nil {
		keys = []string{}
	}
	_ = s.o.Audit.Write(map[string]any{
		"time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "event": "control_call", "actor": "control", "caller": "control:" + connectionIdentity(),
		"device": s.o.Listen.Node, "tool": c.route.ID, "args_digest": map[string]any{"keys": keys, "bytes": c.size},
		"status": status, "error": code, "duration_ms": max(int64(0), time.Since(started).Milliseconds()),
	})
}

func (s *Server) fileDigest() string {
	path := s.o.Store.Path()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sum(raw)
}

func ptr[T any](v T) *T { return &v }

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func etagOf(b []byte) string { return "sha256-" + sum(b) }

// serveStopTimeout bounds how long Close waits for the serving and polling goroutines.
var serveStopTimeout = 5 * time.Second

// stopHTTP shuts srv down and returns when that is done or ctx ends. net/http waits for every
// Serve loop to return in both Shutdown and Close, so a listener that refuses to stop would hold
// the caller for good. Shutdown therefore runs on its own goroutine. When ctx ends first, the
// forced Close also runs on a goroutine, and it ends the connections that are still open.
func stopHTTP(ctx context.Context, srv *http.Server) {
	shut := make(chan error, 1)
	go func() { shut <- srv.Shutdown(ctx) }()
	select {
	case err := <-shut:
		// Shutdown returns only after its Serve loops have returned, so Close cannot block here.
		if err != nil {
			_ = srv.Close()
		}
	case <-ctx.Done():
		go func() { _ = srv.Close() }()
	}
}

// waitTimeout waits for wg and reports whether it finished within d.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-finished:
		return true
	case <-timer.C:
		return false
	}
}
