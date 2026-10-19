package desktopbroker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type desktop struct {
	epoch  string
	closed atomic.Bool
}

func (d *desktop) Info(context.Context) (computer.Info, error) {
	return computer.Info{ProtocolVersion: 1, Available: true, Epoch: d.epoch, DesktopID: "desktop", LayoutVersion: "layout"}, nil
}
func (d *desktop) Capture(context.Context, computer.CaptureRequest) (computer.Frame, error) {
	return computer.Frame{Image: []byte{1, 2, 3}, Epoch: d.epoch}, nil
}
func (d *desktop) Execute(context.Context, computer.Execution) (computer.ActionResult, error) {
	return computer.ActionResult{State: "executed", Executed: 1}, nil
}
func (d *desktop) Release(context.Context, computer.Execution) error { return nil }
func (d *desktop) Close() error                                      { d.closed.Store(true); return nil }

func TestLocalBrokerHasOneKernelOwnerAndRestartsEpoch(t *testing.T) {
	var generation atomic.Int32
	b, err := Start(Options{StateDir: t.TempDir(), Factory: func() (computer.Provider, error) { return &desktop{epoch: fmt.Sprint(generation.Add(1))}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a := computer.NewClient(b.Endpoint)
	defer a.Close()
	other := computer.NewClient(b.Endpoint)
	defer other.Close()
	info, err := a.Info(context.Background())
	if err != nil || info.Epoch != "1" {
		t.Fatal(info, err)
	}
	if _, err = other.Info(context.Background()); err == nil {
		t.Fatal("two kernels acquired a desktop")
	} else if e, ok := err.(*toolerr.Error); !ok || e.Code != "resource_busy" {
		t.Fatal(err)
	}
	f, err := a.Capture(context.Background(), computer.CaptureRequest{})
	if err != nil || len(f.Image) != 3 {
		t.Fatal(f, err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	info, err = other.Info(context.Background())
	if err != nil || info.Epoch != "2" {
		t.Fatal(info, err)
	}
}
