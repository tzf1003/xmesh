package app

import (
	"context"
	"errors"
	"github.com/tzf1003/xmesh/internal/ci"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/identity"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

type deadlineDrainSender struct {
	budget chan time.Duration
}

type aliasDrainSender struct{ received agent.DrainRequest }

func (s *aliasDrainSender) Peers(context.Context) ([]mesh.Peer, error) {
	return []mesh.Peer{{ID: "dev-persistent", Name: "workstation-1234", Online: true, Tags: []string{"tag:device"}}}, nil
}
func (s *aliasDrainSender) SendDrain(_ context.Context, _ mesh.Peer, req agent.DrainRequest) error {
	s.received = req
	return nil
}
func TestRemoteDrainResolvesAliasToPersistentID(t *testing.T) {
	sender := new(aliasDrainSender)
	_, err := remoteDrain(t.Context(), sender, v2.DrainRequest{Scope: "device", Device: "workstation-1234", Mode: "keep"})
	if err != nil || sender.received.Device != "dev-persistent" {
		t.Fatal(sender.received, err)
	}
}

func TestRemoteDrainUpdatesCIRunnersOnlyAfterAdminAuthorization(t *testing.T) {
	mgr := ci.New(ci.Options{StateDir: t.TempDir(), Config: func() config.CI { return config.CI{} }})
	defer mgr.Shutdown(time.Second)
	for _, tag := range []string{"tag:peer", "tag:admin"} {
		handler := deviceDrainControl(func(*http.Request) (identity.Principal, error) {
			return identity.Principal{Node: "caller", Tags: []string{tag}}, nil
		}, "target", "dev-target", nil, newGatewayRegistry(), new(sync.Mutex), func(bool) { mgr.SetDraining(true) })
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/control/drain", strings.NewReader(`{"scope":"device","device":"dev-target","drain":"keep"}`)))
		if mgr.Overview().Draining != (tag == "tag:admin") {
			t.Fatal("CI drain authorization boundary", tag, response.Code, mgr.Overview())
		}
	}
}

func (s deadlineDrainSender) Peers(context.Context) ([]mesh.Peer, error) {
	return []mesh.Peer{{ID: "one", Name: "one", Online: true, Tags: []string{"tag:device"}}}, nil
}
func (s deadlineDrainSender) SendDrain(ctx context.Context, _ mesh.Peer, _ agent.DrainRequest) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("missing deadline")
	}
	s.budget <- time.Until(deadline)
	<-ctx.Done()
	return ctx.Err()
}
func TestRemoteDrainHonorsDeviceTimeout(t *testing.T) {
	sender := deadlineDrainSender{budget: make(chan time.Duration, 1)}
	outcomes, err := remoteDrain(t.Context(), sender, v2.DrainRequest{Scope: "mesh", TimeoutMS: 20})
	budget := <-sender.budget
	if err != nil || len(outcomes) != 1 || !strings.Contains(outcomes[0].Error, "deadline exceeded") || budget <= 0 || budget > 20*time.Millisecond {
		t.Fatal(outcomes, budget, err)
	}
	for _, requested := range []int64{0, 45000} {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := remoteDrain(ctx, sender, v2.DrainRequest{Scope: "mesh", TimeoutMS: requested})
			done <- err
		}()
		budget = <-sender.budget
		expected := time.Duration(requested) * time.Millisecond
		if requested == 0 {
			expected = 15 * time.Second
		}
		cancel()
		if err := <-done; err != nil || budget > expected || budget < expected-time.Second {
			t.Fatal(requested, budget, err)
		}
	}
}

func TestRoleOnlyNodeRemoteDrainUsesAdminBoundary(t *testing.T) {
	for _, item := range []struct {
		tag, device, scope string
		status             int
	}{
		{"tag:peer", "node-id", "device", 403},
		{"tag:admin", "other", "device", 404},
		{"tag:admin", "node-id", "device", 200},
		{"tag:admin", "", "mesh", 200},
	} {
		registry := newGatewayRegistry()
		handler := deviceDrainControl(func(*http.Request) (identity.Principal, error) {
			return identity.Principal{Node: "admin", Tags: []string{item.tag}}, nil
		}, "role-only", "node-id", nil, registry, new(sync.Mutex), nil)
		body := `{"scope":"` + item.scope + `","device":"` + item.device + `","drain":"keep"}`
		request := httptest.NewRequest("POST", "http://device/control/drain", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatal(item, response.Code, response.Body.String())
		}
		registry.mu.Lock()
		pending := len(registry.pending)
		registry.mu.Unlock()
		if (pending > 0) != (item.status == 200) {
			t.Fatal("role drain changed state across a refused request", item, pending)
		}
	}
}

func TestRoleOnlyRemoteDrainIsVisibleAndResumable(t *testing.T) {
	cfg := parseConfig(t, []string{"gateway"}, address(t))
	registry := newGatewayRegistry()
	management := new(sync.Mutex)
	host := &controlHost{d: controlDeps{store: config.Static(cfg), management: management, isDraining: func() bool { return registry.draining(cfg.Node.Name) }, resume: func() { registry.resume(cfg.Node.Name, "") }}, firewall: newFirewallWatch()}
	state := t.TempDir()
	server, err := control.Start(control.Options{Host: host, Store: host.d.store, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	endpoint, err := control.ReadEndpoint(state)
	if err != nil {
		t.Fatal(err)
	}
	client := control.NewClient(endpoint)
	handler := deviceDrainControl(func(*http.Request) (identity.Principal, error) {
		return identity.Principal{Node: "admin", Tags: []string{"tag:admin"}}, nil
	}, cfg.Node.Name, "node-id", nil, registry, management, nil)
	for _, scope := range []string{"device", "mesh"} {
		request := httptest.NewRequest("POST", "http://device/control/drain", strings.NewReader(`{"scope":"`+scope+`","device":"node-id","drain":"keep"}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 || !host.Draining() {
			t.Fatal(scope, response.Code, host.Draining())
		}
		var status v2.StatusResponse
		if err := client.Fetch(t.Context(), "/v2/status", &status); err != nil || !status.Drain.Draining {
			t.Fatal("remote drain not visible", status.Drain, err)
		}
		res, err := client.Do(t.Context(), "POST", "/v2/resume", nil, nil)
		if err != nil || res.Error() != nil || host.Draining() || registry.draining(cfg.Node.Name) {
			t.Fatal("remote drain not resumed", scope, err)
		}
	}
}
