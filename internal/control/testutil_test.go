package control

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	warnings []string
	ptys     []map[string]any
	peers    []Peer
}

func (h *fakeHost) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Status{Node: "ctl-node", Roles: []string{"agent"}, Draining: h.draining, Warnings: append([]string{}, h.warnings...), Backends: []Backend{}, Workspaces: []Workspace{}, Limits: map[string]any{"pty_in_use": len(h.ptys)}}
}
func (h *fakeHost) Peers(context.Context) (Peers, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Peers{Mode: "lan", Peers: append([]Peer{}, h.peers...)}, nil
}
func (h *fakeHost) Sessions() Sessions {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Sessions{PTY: append([]map[string]any{}, h.ptys...), Exec: map[string]int{"shared_in_use": 0}}
}
func (h *fakeHost) Drain(kill bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = true
	h.kills = append(h.kills, kill)
}
func (h *fakeHost) Resume() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = false
}
func (h *fakeHost) mutate(f func(*fakeHost)) { h.mu.Lock(); f(h); h.mu.Unlock() }

type fixture struct {
	t      *testing.T
	server *Server
	client *Client
	host   *fakeHost
	store  *config.Store
	dir    string
	path   string
}

// newFixture starts a server on a fresh state directory.
func newFixture(t *testing.T, mutate ...func(*Options)) *fixture {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "s")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "xmesh.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0600); err != nil {
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

// decode returns the body as a generic object.
func decode(t *testing.T, r *Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("%s: %v", r.Body, err)
	}
	return out
}

// stream reads parsed events from /v1/events until stop is called.
type stream struct {
	events chan Event
	stop   context.CancelFunc
}

func (f *fixture) stream(lastID string) *stream {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	headers := map[string]string{}
	if lastID != "" {
		headers["Last-Event-ID"] = lastID
	}
	res, err := f.client.Stream(ctx, "/v1/events", headers)
	if err != nil {
		cancel()
		f.t.Fatal(err)
	}
	if res.StatusCode != 200 {
		cancel()
		f.t.Fatalf("events: HTTP %d", res.StatusCode)
	}
	s := &stream{events: make(chan Event, 256), stop: cancel}
	go func() {
		defer res.Body.Close()
		defer close(s.events)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e Event
				if json.Unmarshal([]byte(data), &e) == nil {
					s.events <- e
				}
			}
		}
	}()
	f.t.Cleanup(cancel)
	return s
}

// next waits for an event matching match.
func (s *stream) next(t *testing.T, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
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
func ofType(kind string) func(Event) bool { return func(e Event) bool { return e.Type == kind } }

func staticStore(t *testing.T) *config.Store {
	t.Helper()
	c, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	return config.Static(c)
}
