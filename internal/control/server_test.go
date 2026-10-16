package control

import (
	"os"
	"strings"
	"testing"
)

// conformance: CON-ARCH-09 the control channel answers over a local socket or pipe only
func Test_ControlStatus(t *testing.T) {
	f := newFixture(t)
	r := f.do("GET", "/v1/status", nil, nil)
	if r.Status != 200 || r.Header.Get(VersionHeader) != APIVersion {
		t.Fatalf("status %d header %q", r.Status, r.Header.Get(VersionHeader))
	}
	st := decode(t, r)
	if st["node"] != "ctl-node" || st["version"] != "test" || st["control_api"] != APIVersion || st["health"] != "ok" || st["pid"] != float64(os.Getpid()) {
		t.Fatalf("unexpected status: %v", st)
	}
	f.host.mutate(func(h *fakeHost) { h.warnings = []string{"x"} })
	if st = decode(t, f.do("GET", "/v1/status", nil, nil)); st["health"] != "degraded" {
		t.Fatalf("warnings should degrade health: %v", st)
	}
	f.host.mutate(func(h *fakeHost) { h.draining = true })
	if st = decode(t, f.do("GET", "/v1/status", nil, nil)); st["health"] != "draining" {
		t.Fatalf("draining wins: %v", st)
	}
}

func Test_ControlRoutesAndErrors(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/v1/nope", 404, "not_found"},
		{"POST", "/v1/status", 405, "invalid_args"},
		{"GET", "/v1/drain", 405, "invalid_args"},
		{"DELETE", "/v1/config", 405, "invalid_args"},
		{"GET", "/mcp", 404, "not_found"},
		{"GET", "/control/drain", 404, "not_found"},
	} {
		r := f.do(c.method, c.path, nil, nil)
		if r.Status != c.status || decode(t, r)["error"] != c.code {
			t.Errorf("%s %s: got %d %s", c.method, c.path, r.Status, r.Body)
		}
	}
}

func Test_ControlRejectsBrowserRequests(t *testing.T) {
	f := newFixture(t)
	for _, h := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		if r := f.do("GET", "/v1/status", nil, h); r.Status != 403 || decode(t, r)["error"] != "unauthorized" {
			t.Errorf("%v: %d %s", h, r.Status, r.Body)
		}
	}
}

func Test_ControlPeersAndSessions(t *testing.T) {
	f := newFixture(t)
	f.host.mutate(func(h *fakeHost) {
		h.peers = []Peer{{ID: "n1", Name: "box", Tags: []string{"tag:device"}, Online: true, Direct: true}}
		h.ptys = []map[string]any{{"id": "p-1", "state": "active", "owner": "n1/s"}}
	})
	peers := decode(t, f.do("GET", "/v1/peers", nil, nil))
	if peers["mode"] != "lan" || len(peers["peers"].([]any)) != 1 {
		t.Fatal(peers)
	}
	sessions := decode(t, f.do("GET", "/v1/sessions", nil, nil))
	if len(sessions["pty"].([]any)) != 1 || sessions["backends"] == nil {
		t.Fatal(sessions)
	}
}

func Test_ControlDrainResume(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	if r := f.do("POST", "/v1/drain", map[string]string{"mode": "bogus"}, nil); r.Status != 400 {
		t.Fatalf("bad mode accepted: %d", r.Status)
	}
	if r := f.do("POST", "/v1/drain", map[string]string{"mode": "kill"}, nil); r.Status != 200 {
		t.Fatalf("%s", r.Body)
	}
	if r := f.do("POST", "/v1/drain", nil, nil); r.Status != 200 {
		t.Fatalf("empty body means keep: %s", r.Body)
	}
	f.host.mu.Lock()
	kills := append([]bool{}, f.host.kills...)
	f.host.mu.Unlock()
	if len(kills) != 2 || !kills[0] || kills[1] {
		t.Fatalf("drain modes: %v", kills)
	}
	if st := decode(t, f.do("GET", "/v1/status", nil, nil)); st["draining"] != true || st["health"] != "draining" {
		t.Fatal(st)
	}
	events.next(t, func(e Event) bool {
		return e.Type == "lifecycle" && e.Data["state"] == "draining" && e.Data["mode"] == "kill"
	})
	if r := f.do("POST", "/v1/resume", nil, nil); r.Status != 200 {
		t.Fatalf("%s", r.Body)
	}
	if st := decode(t, f.do("GET", "/v1/status", nil, nil)); st["draining"] != false {
		t.Fatal(st)
	}
	events.next(t, func(e Event) bool { return e.Type == "lifecycle" && e.Data["state"] == "resumed" })
}

func Test_ControlEndpointFile(t *testing.T) {
	f := newFixture(t)
	ep, err := ReadEndpoint(f.server.o.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Node != "ctl-node" || ep.PID != os.Getpid() || ep.ControlAPI != APIVersion || ep.KernelVersion != "test" || ep.Address != f.server.Endpoint().Address {
		t.Fatalf("%+v", ep)
	}
	if st, err := os.Stat(f.server.o.StateDir + "/" + FileName); err != nil || (os.PathSeparator == '/' && st.Mode().Perm() != 0600) {
		t.Fatalf("control.json mode: %v %v", st, err)
	}
	f.server.Close()
	if _, err = ReadEndpoint(f.server.o.StateDir); err == nil {
		t.Fatal("control.json should be removed on close")
	}
	if _, err = NewClient(ep).Do(t.Context(), "GET", "/v1/status", nil, nil); err == nil {
		t.Fatal("channel still answering after close")
	}
}

func Test_ControlDiscover(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "AppData"} {
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
		if err = WriteEndpoint(dir, Endpoint{Transport: "unix", Address: "/x", Node: node}); err != nil {
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
