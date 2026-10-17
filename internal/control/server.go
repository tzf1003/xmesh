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

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
)

type Options struct {
	Host    Host
	Store   *config.Store
	Version string
	// StateDir receives control.json; AuditDir is read by GET /v1/audit.
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
}

func (o Options) heartbeat() time.Duration {
	if o.Heartbeat > 0 {
		return o.Heartbeat
	}
	return v1.HeartbeatSeconds * time.Second
}

// call is what a handler reports back for the audit record of its request.
type call struct {
	route v1.Route
	// keys are the top-level keys of the request body; size its length in bytes.
	keys []string
	size int
}

type handler func(w http.ResponseWriter, r *http.Request, c *call)

type Server struct {
	o        Options
	bound    Bound
	endpoint v1.ControlEndpointFile
	srv      *http.Server
	bus      *Bus
	started  v1.Time
	caps     []v1.Capability
	routes   []route
	handlers map[string]handler
	stop     chan struct{}
	done     sync.WaitGroup
	once     sync.Once

	lifecycle    atomic.Value // string
	auditDropped atomic.Bool

	configMu   sync.Mutex // serialises PUT /v1/config and guards the fields below
	digest     string
	lastParsed map[string]any
	failures   uint64

	drainMu sync.Mutex
	drain   v1.DrainState

	peerMu sync.Mutex
	peers  []v1.Peer
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
	s := &Server{o: o, bound: bound, bus: NewBus(), started: v1.Now(), stop: make(chan struct{})}
	s.lifecycle.Store("starting")
	s.caps = capabilities(o)
	s.routes = compileRoutes(v1.Routes())
	s.handlers = s.table()
	s.digest = s.fileDigest()
	if path := o.Store.Path(); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			s.lastParsed = parsedOf(raw)
		}
	}
	_, s.failures = o.Store.State()
	s.endpoint = v1.ControlEndpointFile{
		APIVersion: v1.APIVersion, KernelVersion: o.Version, Node: o.Listen.Node, PID: os.Getpid(), BootID: s.bus.BootID(),
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
func capabilities(o Options) []v1.Capability {
	caps := []v1.Capability{v1.CapStatus, v1.CapEvents, v1.CapPeers, v1.CapSessions, v1.CapDrain}
	if o.Store.Path() != "" {
		caps = append(caps, v1.CapConfigRead, v1.CapConfigWrite, v1.CapPats)
	}
	return append(caps, v1.CapPair, v1.CapIngress, v1.CapAudit)
}

func (s *Server) hasCapability(c v1.Capability) bool {
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
		if s.srv.Shutdown(ctx) != nil {
			_ = s.srv.Close()
		}
		s.done.Wait()
		RemoveEndpoint(s.o.StateDir, s.endpoint.PID)
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
	data := v1.CallEventData{Tool: text("tool"), Device: text("device"), Caller: text("caller"), Status: text("status")}
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
	_ = s.bus.Publish(v1.EventCall, data)
}

func (s *Server) lifecycleEvent(change string) {
	_ = s.bus.Publish(v1.EventLifecycle, v1.LifecycleEventData{Change: change, Drain: s.currentDrain()})
}

// route is one row of the route table with its path split for matching.
type route struct {
	v1.Route
	parts []string
}

func compileRoutes(table []v1.Route) []route {
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
		"control.status.get":    s.status,
		"control.events.stream": func(w http.ResponseWriter, r *http.Request, _ *call) { s.events(w, r) },
		"control.peers.list":    s.listPeers,
		"control.sessions.list": s.sessions,
		"control.drain.start":   s.startDrain,
		"control.drain.resume":  s.resume,
		"control.config.get":    s.getConfig,
		"control.config.put":    s.putConfig,
		"control.audit.list":    s.auditList,

		"control.pair.export":            s.pairExport,
		"control.pair.import":            s.pairImport,
		"control.pats.list":              s.listPats,
		"control.pats.create":            s.createPat,
		"control.pats.rotate":            s.rotatePat,
		"control.pats.delete":            s.deletePat,
		"control.ingress.clients.list":   s.listIngressClients,
		"control.ingress.clients.delete": s.deleteIngressClient,
		"control.ingress.password.put":   s.putIngressPassword,
	}
}

// recorder remembers the status and error code of a response for the audit record.
type recorder struct {
	http.ResponseWriter
	status int
	code   v1.ErrorCode
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
func failWith(w http.ResponseWriter, e *v1.Error) {
	if rec, ok := w.(*recorder); ok {
		rec.code = e.Code
	}
	fail(w, e)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	rec := &recorder{ResponseWriter: w}
	w = rec
	w.Header().Set(v1.HeaderVersion, v1.APIVersion)
	w.Header().Set("Cache-Control", "no-store")
	// Browsers cannot reach a socket or pipe; a request that looks like one is wrong.
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		failWith(w, newError(v1.ErrForbidden, "控制通道不接受来自网页的请求"))
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
		failWith(w, newError(v1.ErrNotFound, "没有这个控制接口"))
		return
	}
	h := s.handlers[matched.ID]
	if h == nil || !s.hasCapability(matched.Capability) {
		failWith(w, newError(v1.ErrNotImplemented, "此内核尚未实现该控制接口"))
		if matched.Method != "GET" {
			s.auditCall(&call{route: matched.Route}, rec, time.Now())
		}
		return
	}
	limit := int64(v1.MaxRequestBytes)
	if matched.ID == "control.config.put" {
		limit = v1.MaxConfigBytes
	}
	c := &call{route: matched.Route}
	started := time.Now()
	if matched.Method != "GET" {
		// Buffer the body once so the audit record can state its size and keys.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				failWith(w, newError(v1.ErrPayloadTooLarge, "请求体过大"))
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
func decode(r *http.Request, v any, required bool) *v1.Error {
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
			code = string(v1.ErrInternal)
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
