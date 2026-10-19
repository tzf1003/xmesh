package control

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/computer"
)

type controlDesktop struct{}

func (controlDesktop) Info(context.Context) (computer.Info, error) {
	return computer.Info{ProtocolVersion: 1, Available: true, State: "ready", DesktopID: "desktop", Epoch: "epoch", LayoutVersion: "layout", Capabilities: []string{"screenshot", "pointer", "keyboard"}, Displays: []computer.Display{}}, nil
}
func (controlDesktop) Capture(context.Context, computer.CaptureRequest) (computer.Frame, error) {
	return computer.Frame{}, nil
}
func (controlDesktop) Execute(context.Context, computer.Execution) (computer.ActionResult, error) {
	return computer.ActionResult{}, nil
}
func (controlDesktop) Release(context.Context, computer.Execution) error { return nil }
func (controlDesktop) Close() error                                      { return nil }

func TestComputerLocalPauseRevokeAndSchema(t *testing.T) {
	var manager *computer.Manager
	f := newFixtureConfig(t, strings.Replace(testConfig, "agent:\n", "agent:\n  computer:\n    enabled: true\n", 1), func(o *Options) {
		manager = computer.New(controlDesktop{}, o.Store.Get().Agent.Computer)
		o.Computer = manager
		t.Cleanup(func() { _ = manager.Close() })
	})
	s, err := manager.Open(context.Background(), "agent/session", computer.OpenRequest{Mode: "control"})
	if err != nil {
		t.Fatal(err)
	}
	r := f.ok("GET", "/v1/computer", nil, "ComputerState")
	if strings.Contains(string(r.Body), "lease_token") {
		t.Fatal("local UI received lease token")
	}
	f.ok("POST", "/v1/computer/revoke", v1.ComputerRevokeRequest{SessionID: s.ID}, "ComputerState")
	if len(manager.Sessions("")) != 0 {
		t.Fatal("revoke retained control")
	}
	_, err = manager.Open(context.Background(), "agent/session", computer.OpenRequest{Mode: "control"})
	if err != nil {
		t.Fatal(err)
	}
	f.ok("POST", "/v1/computer/pause", v1.ComputerPauseRequest{Paused: true}, "ComputerState")
	if len(manager.Sessions("")) != 0 {
		t.Fatal("takeover retained control")
	}
	if _, err = manager.Open(context.Background(), "agent/session", computer.OpenRequest{Mode: "control"}); err == nil {
		t.Fatal("paused control opened")
	}
	f.ok("POST", "/v1/computer/pause", v1.ComputerPauseRequest{Paused: false}, "ComputerState")
}
