package main

import (
	"bytes"
	"context"
	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeNetwork struct{ peers []mesh.Peer }

func TestDrainReportsPartialVersion2Outcomes(t *testing.T) {
	var out bytes.Buffer
	err := printDrainOutcomes(&out, []v2.DrainOutcome{{Device: "one"}, {Device: "two", Error: "permission denied"}}, agent.DrainRequest{Scope: "mesh", Drain: "keep"})
	if err == nil || !strings.Contains(out.String(), "已下线 one") || !strings.Contains(out.String(), "失败 two") {
		t.Fatal(out.String(), err)
	}
}

func (n fakeNetwork) Peers(context.Context) ([]mesh.Peer, error) { return n.peers, nil }
func (n fakeNetwork) HTTPClient(mesh.Peer) *http.Client          { return http.DefaultClient }

func Test_CTRGW05_DrainCommandRequest(t *testing.T) {
	for _, c := range []struct {
		scope, device, mode string
		ok                  bool
	}{
		{"device", "one", "keep", true}, {"mesh", "", "kill", true},
		{"device", "", "keep", false}, {"mesh", "one", "keep", false}, {"", "", "keep", false}, {"mesh", "", "other", false},
	} {
		if _, e := drainRequest(c.scope, c.device, c.mode); (e == nil) != c.ok {
			t.Fatal(c, e)
		}
	}
}

func Test_CTRGW05_DrainCommandFansOut(t *testing.T) {
	var hits atomic.Int32
	bodies := make(chan string, 4)
	handler := func(status int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.Path != "/control/drain" || r.Method != "POST" {
				t.Error(r.Method, r.URL.Path)
			}
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(r.Body)
			bodies <- buf.String()
			w.WriteHeader(status)
		})
	}
	good, bad := httptest.NewServer(handler(200)), httptest.NewServer(handler(403))
	defer good.Close()
	defer bad.Close()
	device := func(name, url string, tags ...string) mesh.Peer {
		return mesh.Peer{Name: name, Address: url + "/mcp", Tags: tags}
	}
	network := fakeNetwork{peers: []mesh.Peer{
		device("alpha", good.URL, "tag:device"),
		device("beta", bad.URL, "tag:device"),
		device("laptop", good.URL, "tag:admin"),
	}}
	var out bytes.Buffer
	err := runDrain(context.Background(), &out, network, agent.DrainRequest{Scope: "mesh", Drain: "kill"}, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "1/2") {
		t.Fatal("a partial drain must fail the command", err)
	}
	if hits.Load() != 2 {
		t.Fatal("only tag:device peers are drained, got", hits.Load())
	}
	if !strings.Contains(out.String(), "已下线 alpha") || !strings.Contains(out.String(), "失败 beta") {
		t.Fatal(out.String())
	}
	if body := <-bodies; !strings.Contains(body, `"scope":"mesh"`) || !strings.Contains(body, `"drain":"kill"`) {
		t.Fatal(body)
	}
	<-bodies
	// A single device is addressed by name.
	hits.Store(0)
	out.Reset()
	if err = runDrain(context.Background(), &out, network, agent.DrainRequest{Scope: "device", Device: "alpha", Drain: "keep"}, 5*time.Second); err != nil || hits.Load() != 1 {
		t.Fatal(err, hits.Load())
	}
	if body := <-bodies; !strings.Contains(body, `"device":"alpha"`) {
		t.Fatal(body)
	}
	if err = runDrain(context.Background(), &out, network, agent.DrainRequest{Scope: "device", Device: "missing"}, time.Second); err == nil {
		t.Fatal("unknown device accepted")
	}
	if err = runDrain(context.Background(), &out, fakeNetwork{}, agent.DrainRequest{Scope: "mesh"}, time.Second); err == nil {
		t.Fatal("an empty mesh must not look drained")
	}
}

type cliRemoteDrainHost struct {
	cliHost
	received chan v2.DrainRequest
}

func (h *cliRemoteDrainHost) RemoteDrain(_ context.Context, req v2.DrainRequest) ([]v2.DrainOutcome, error) {
	h.received <- req
	return []v2.DrainOutcome{{Device: "remote"}}, nil
}
func TestDrainCommandPassesTimeoutToRunningNode(t *testing.T) {
	host := &cliRemoteDrainHost{received: make(chan v2.DrainRequest, 1)}
	configText := "schema_version: 2\n" + strings.Replace(cliConfig, "mesh:\n  mode: lan\n  lan:\n    mdns: true\n", "mesh: {}\n", 1)
	state, configPath := startKernelStubOptions(t, configText, func(o *control.Options) { o.Host = host })
	for _, item := range []struct {
		flag         string
		milliseconds int64
	}{{"1s", 1000}, {"45s", 45000}, {"1ns", 1}} {
		out, err := output(t, func() error {
			return drain([]string{"--config", configPath, "--state-dir", state, "--scope", "mesh", "--timeout", item.flag})
		})
		if err != nil || !strings.Contains(out, "已下线 remote") {
			t.Fatal(out, err)
		}
		req := <-host.received
		if req.TimeoutMS != item.milliseconds {
			t.Fatal(item, req)
		}
	}
	for _, value := range []string{"0", "-1s"} {
		if err := drain([]string{"--scope", "mesh", "--timeout", value}); err == nil {
			t.Fatal("accepted timeout", value)
		}
	}
}
