package control

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/api/control/v2/schemacheck"
	"github.com/tzf1003/xmesh/internal/config"
)

const testConfig = `# 节点配置
node:
  name: ctl-node   # 节点名
  roles: [agent]
mesh:
  mode: lan
  lan:
    mdns: true
agent:
  default_cwd: .
  policy:
    allow_dangerous: false # 危险命令开关
  cwd_policy:
    allow: ["."]
`

// fakeHost records drain/resume and serves canned state.
type fakeHost struct {
	mu       sync.Mutex
	draining bool
	kills    []bool
	warnings []v2.Warning
	ptys     []v2.PtySession
	execs    []v2.ExecRun
	leases   []v2.BackendLease
	peers    []v2.Peer
}

func (h *fakeHost) Status() v2.StatusResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	return v2.StatusResponse{
		OS: "linux", Arch: "amd64", Hostname: "ctl-node", Home: "/home/x", Roles: []string{"agent"}, PathSep: "/",
		Network:  v2.NetworkIdentity{Mode: "lan", NodeID: "ctl-node", Tags: []string{"tag:peer"}},
		Warnings: append([]v2.Warning{}, h.warnings...), Shells: []string{"bash"}, Workspaces: []v2.Workspace{}, Backends: []v2.Backend{},
		Limits: v2.Limits{ExecShared: 4, ExecExclusive: 1, PTYMaxSessions: 20, PTYMaxPerCaller: 8, PTYInUse: len(h.ptys), PTYClosedRetainMax: 20},
	}
}
func (h *fakeHost) Draining() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.draining }
func (h *fakeHost) Peers(context.Context) ([]v2.Peer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	self := v2.Peer{Name: "ctl-node", Self: true, NodeID: "ctl-node", Tags: []string{"tag:peer"}, Online: true, Connection: "local", Addrs: []string{}}
	return append([]v2.Peer{self}, h.peers...), nil
}
func (h *fakeHost) Sessions() v2.SessionsResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	return v2.SessionsResponse{PTY: append([]v2.PtySession{}, h.ptys...), Exec: append([]v2.ExecRun{}, h.execs...), Leases: append([]v2.BackendLease{}, h.leases...)}
}
func (h *fakeHost) Drain(kill bool) (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = true
	h.kills = append(h.kills, kill)
	if kill {
		return len(h.ptys), len(h.leases)
	}
	return 0, 0
}
func (h *fakeHost) Resume() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = false
}
func (h *fakeHost) mutate(f func(*fakeHost)) { h.mu.Lock(); f(h); h.mu.Unlock() }

func peer(name string, online bool) v2.Peer {
	return v2.Peer{Name: name, NodeID: name + "-id", Tags: []string{"tag:device"}, Online: online, Connection: "direct", Addrs: []string{"100.64.0.2"}}
}
func pty(id string) v2.PtySession {
	return v2.PtySession{Kind: "pty", ID: id, Owner: "n1/s", PID: 10, State: "active", Cols: 80, Rows: 24, Cwd: "/work", CreatedAt: v2.Now()}
}

type fixture struct {
	t      *testing.T
	server *Server
	client *Client
	host   *fakeHost
	store  *config.Store
	dir    string
	path   string
}

// shortTempDir is t.TempDir for tests that bind a Unix socket: macOS temp paths are long enough to
// overflow sun_path (104 bytes), so the directory goes under /tmp there.
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "xmc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newFixture starts a server on a fresh state directory.
func newFixture(t *testing.T, mutate ...func(*Options)) *fixture {
	t.Helper()
	return newFixtureConfig(t, testConfig, mutate...)
}

// newFixtureConfig is newFixture with an explicit configuration file.
func newFixtureConfig(t *testing.T, configText string, mutate ...func(*Options)) *fixture {
	t.Helper()
	dir := shortTempDir(t)
	state := filepath.Join(dir, "s")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "xmesh.yaml")
	if err := os.WriteFile(path, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{}
	o := Options{Host: host, Store: store, Version: "test", StateDir: state, AuditDir: filepath.Join(dir, "audit"), Poll: 20 * time.Millisecond}
	for _, m := range mutate {
		m(&o)
	}
	s, err := Start(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &fixture{t: t, server: s, client: NewClient(s.Endpoint()), host: host, store: store, dir: dir, path: path}
}

func (f *fixture) do(method, path string, body any, headers map[string]string) *Response {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := f.client.Do(ctx, method, path, body, headers)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// get and ok fetch a path and require a 200 whose body satisfies the schema definition def.
func (f *fixture) ok(method, path string, body any, def string, headers ...map[string]string) *Response {
	f.t.Helper()
	h := map[string]string{}
	for _, extra := range headers {
		for k, v := range extra {
			h[k] = v
		}
	}
	r := f.do(method, path, body, h)
	if r.Status != 200 {
		f.t.Fatalf("%s %s: %d %s", method, path, r.Status, r.Body)
	}
	valid(f.t, def, r.Body)
	return r
}

// valid fails the test when doc does not satisfy the contract schema definition def.
func valid(t *testing.T, def string, doc []byte) {
	t.Helper()
	if err := schemacheck.Validate(def, doc); err != nil {
		t.Fatalf("%s violates the contract: %v\n%s", def, err, doc)
	}
}

// apiError requires an error response with the given code and the HTTP status the contract gives it.
func apiError(t *testing.T, r *Response, code v2.ErrorCode) *v2.Error {
	t.Helper()
	var e v2.Error
	if err := json.Unmarshal(r.Body, &e); err != nil || e.Code != code || r.Status != code.HTTPStatus() {
		t.Fatalf("want %s (%d), got %d %s", code, code.HTTPStatus(), r.Status, r.Body)
	}
	valid(t, "ControlError", r.Body)
	return &e
}

// decodeAs unmarshals a response body into out.
func decodeAs(t *testing.T, r *Response, out any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, out); err != nil {
		t.Fatalf("%s: %v", r.Body, err)
	}
}

// stream reads parsed events from /v2/events until stop is called. Every event is checked against
// the ControlEvent schema as it arrives.
type stream struct {
	events chan v2.Event
	stop   context.CancelFunc
}

func (f *fixture) stream(lastID string) *stream {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	headers := map[string]string{}
	if lastID != "" {
		headers["Last-Event-ID"] = lastID
	}
	res, err := f.client.Stream(ctx, "/v2/events", headers)
	if err != nil {
		cancel()
		f.t.Fatal(err)
	}
	if res.StatusCode != 200 {
		cancel()
		f.t.Fatalf("events: HTTP %d", res.StatusCode)
	}
	s := &stream{events: make(chan v2.Event, 512), stop: cancel}
	go func() {
		defer res.Body.Close()
		defer close(s.events)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		var id, kind, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && data != "":
				// an event counts once its blank line has arrived; a line cut off by the
				// connection closing is never dispatched
				var e v2.Event
				if err := json.Unmarshal([]byte(data), &e); err != nil {
					f.t.Errorf("bad event %q: %v", data, err)
				} else if err = schemacheck.Validate("ControlEvent", []byte(data)); err != nil {
					f.t.Errorf("event violates the contract: %v\n%s", err, data)
				} else if string(e.Type) != kind || e.ID != id {
					f.t.Errorf("SSE fields id=%q event=%q do not match data %s", id, kind, data)
				}
				s.events <- e
				id, kind, data = "", "", ""
			}
		}
	}()
	f.t.Cleanup(cancel)
	return s
}

// next waits for an event matching match.
func (s *stream) next(t *testing.T, match func(v2.Event) bool) v2.Event {
	t.Helper()
	deadline := time.After(20 * time.Second) // a slow Windows runner can need seconds to start sampling
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				t.Fatal("event stream closed")
			}
			if match(e) {
				return e
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}
func ofType(kind v2.EventType) func(v2.Event) bool {
	return func(e v2.Event) bool { return e.Type == kind }
}

// payload decodes an event's data into out.
func payload(t *testing.T, e v2.Event, out any) {
	t.Helper()
	if err := json.Unmarshal(e.Data, out); err != nil {
		t.Fatal(err)
	}
}

func staticStore(t *testing.T) *config.Store {
	t.Helper()
	c, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	return config.Static(c)
}
