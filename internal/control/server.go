package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type Options struct {
	Host    Host
	Store   *config.Store
	Version string
	// StateDir receives control.json; AuditDir is read by GET /v1/audit.
	StateDir, AuditDir string
	Listen             ListenOptions
	// Audit receives actor=control records for changes made through the channel.
	Audit *audit.Logger
	// Poll is the state sampling interval behind the event stream. Default 1s.
	Poll time.Duration
}

type Server struct {
	o        Options
	endpoint Endpoint
	srv      *http.Server
	bus      *Bus
	started  time.Time
	stop     chan struct{}
	done     sync.WaitGroup
	once     sync.Once
	configMu sync.Mutex // serialises PUT /v1/config and guards digest
	digest   string
}

// Start opens the channel, writes control.json and begins serving. A node
// without a UI keeps working if this fails, so callers should log and continue.
func Start(o Options) (*Server, error) {
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	o.Listen.StateDir, o.Listen.Node = o.StateDir, o.Store.Get().Node.Name
	listener, endpoint, err := Listen(o.Listen)
	if err != nil {
		return nil, err
	}
	s := &Server{o: o, bus: NewBus(), started: time.Now(), stop: make(chan struct{})}
	endpoint.Node, endpoint.PID, endpoint.KernelVersion, endpoint.ControlAPI = o.Listen.Node, os.Getpid(), o.Version, APIVersion
	endpoint.StartedAt = s.started.UTC().Format(time.RFC3339)
	s.endpoint = endpoint
	s.digest = s.fileDigest()
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	if err = WriteEndpoint(o.StateDir, endpoint); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s.done.Add(2)
	go func() {
		defer s.done.Done()
		_ = s.srv.Serve(listener)
	}()
	go func() {
		defer s.done.Done()
		s.poll()
	}()
	_ = s.bus.Publish("lifecycle", map[string]any{"state": "started"})
	return s, nil
}
func (s *Server) Endpoint() Endpoint { return s.endpoint }
func (s *Server) Bus() *Bus          { return s.bus }

// Close ends the stream, closes the listener and removes control.json.
func (s *Server) Close() {
	s.once.Do(func() {
		_ = s.bus.Publish("lifecycle", map[string]any{"state": "stopping"})
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

// Observe turns an audit record into an event. It is registered with the audit
// logger and must not block.
func (s *Server) Observe(record map[string]any) {
	switch record["event"] {
	case "policy_reload_failed", "audit_dropped":
		_ = s.bus.Publish("warning", map[string]any{"message": fmt.Sprint(record["event"]), "active": true})
		return
	}
	data := map[string]any{}
	for _, key := range []string{"event", "caller", "oauth_client", "device", "tool", "status", "error", "duration_ms", "actor"} {
		if v, ok := record[key]; ok && v != nil {
			data[key] = v
		}
	}
	_ = s.bus.Publish("call", data)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(VersionHeader, APIVersion)
	w.Header().Set("Cache-Control", "no-store")
	// Browsers cannot reach a socket or pipe; a request that looks like one is wrong.
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		fail(w, 403, toolerr.New("unauthorized", "控制通道不接受来自网页的请求"))
		return
	}
	route := func(method string, h http.HandlerFunc) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			fail(w, 405, toolerr.New("invalid_args", "请求方法不支持", "field", "method"))
			return
		}
		h(w, r)
	}
	switch r.URL.Path {
	case "/v1/status":
		route("GET", s.status)
	case "/v1/peers":
		route("GET", s.peers)
	case "/v1/sessions":
		route("GET", s.sessions)
	case "/v1/events":
		route("GET", s.events)
	case "/v1/audit":
		route("GET", s.auditView)
	case "/v1/drain":
		route("POST", s.drain)
	case "/v1/resume":
		route("POST", s.resume)
	case "/v1/config":
		switch r.Method {
		case "GET":
			s.getConfig(w, r)
		case "PUT":
			s.putConfig(w, r)
		default:
			w.Header().Set("Allow", "GET, PUT")
			fail(w, 405, toolerr.New("invalid_args", "请求方法不支持", "field", "method"))
		}
	default:
		fail(w, 404, toolerr.New("not_found", "没有这个控制接口", "name", r.URL.Path))
	}
}

// snapshot completes the host's status with what only the server knows.
func (s *Server) snapshot() Status {
	st := s.o.Host.Status()
	st.Version, st.ControlAPI, st.PID = s.o.Version, APIVersion, os.Getpid()
	st.StartedAt = s.started.UTC().Format(time.RFC3339)
	st.OS, st.Arch = runtime.GOOS, runtime.GOARCH
	if st.Node == "" {
		st.Node = s.o.Listen.Node
	}
	if st.Warnings == nil {
		st.Warnings = []string{}
	}
	st.Health = "ok"
	if len(st.Warnings) > 0 {
		st.Health = "degraded"
	}
	if st.Draining {
		st.Health = "draining"
	}
	return st
}
func (s *Server) status(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.snapshot()) }
func (s *Server) peers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := s.o.Host.Peers(ctx)
	if err != nil {
		fail(w, 500, toolerr.Internal("control.peers", err))
		return
	}
	if p.Peers == nil {
		p.Peers = []Peer{}
	}
	writeJSON(w, 200, p)
}
func (s *Server) sessions(w http.ResponseWriter, _ *http.Request) {
	v := s.o.Host.Sessions()
	if v.PTY == nil {
		v.PTY = []map[string]any{}
	}
	if v.Backends == nil {
		v.Backends = []BackendLease{}
	}
	writeJSON(w, 200, v)
}

func (s *Server) drain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			fail(w, 400, toolerr.Args("body"))
			return
		}
	}
	if req.Mode != "" && req.Mode != "keep" && req.Mode != "kill" {
		fail(w, 400, toolerr.Args("mode"))
		return
	}
	started := time.Now()
	s.o.Host.Drain(req.Mode == "kill")
	mode := req.Mode
	if mode == "" {
		mode = "keep"
	}
	s.audit("control.drain", "", []string{"mode"}, started)
	_ = s.bus.Publish("lifecycle", map[string]any{"state": "draining", "mode": mode})
	writeJSON(w, 200, map[string]any{"ok": true, "draining": true, "mode": mode})
}
func (s *Server) resume(w http.ResponseWriter, _ *http.Request) {
	started := time.Now()
	s.o.Host.Resume()
	s.audit("control.resume", "", nil, started)
	_ = s.bus.Publish("lifecycle", map[string]any{"state": "resumed"})
	writeJSON(w, 200, map[string]any{"ok": true, "draining": false})
}

// audit records a change made through the channel (actor=control). Only key
// names and counts go in, never values (CTR-AUD-01, INV-10).
func (s *Server) audit(tool, errCode string, keys []string, started time.Time) {
	if s.o.Audit == nil {
		return
	}
	status, code := "ok", any(nil)
	if errCode != "" {
		status, code = "error", errCode
	}
	if keys == nil {
		keys = []string{}
	}
	_ = s.o.Audit.Write(map[string]any{"time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "event": "control", "caller": "control/local", "device": s.o.Listen.Node, "tool": tool, "args_digest": map[string]any{"keys": keys}, "status": status, "error": code, "duration_ms": max(int64(0), time.Since(started).Milliseconds()), "actor": "control"})
}

func sum(b []byte) string    { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func etagOf(b []byte) string { return `"` + sum(b) + `"` }
func unquote(tag string) string {
	return strings.Trim(strings.TrimPrefix(strings.TrimSpace(tag), "W/"), `"`)
}
