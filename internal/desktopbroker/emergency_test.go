package desktopbroker

import (
	"context"
	"github.com/tzf1003/xmesh/internal/computer"
	"testing"
)

func TestLocalEmergencyStopsWithoutKernelAndRequiresLocalResume(t *testing.T) {
	first := &desktop{epoch: "initial"}
	starts := 0
	b, err := Start(Options{StateDir: t.TempDir(), Factory: func() (computer.Provider, error) {
		starts++
		if starts == 1 {
			return first, nil
		}
		return &desktop{epoch: "restored"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	owner := computer.NewClient(b.Endpoint)
	defer owner.Close()
	local := computer.NewClient(b.Endpoint)
	defer local.Close()
	ctx := context.Background()
	if _, err := owner.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if err := local.EmergencyStop(ctx); err != nil {
		t.Fatal(err)
	}
	if !first.closed.Load() {
		t.Fatal("native process retained input authority")
	}
	if _, err := owner.Info(ctx); err == nil {
		t.Fatal("suspended host restarted native executor")
	}
	if status, err := local.HostStatus(ctx); err != nil || !status.Suspended || status.NativePID != 0 {
		t.Fatal(status, err)
	}
	if starts != 1 {
		t.Fatal("remote traffic resumed sharing")
	}
	if err := local.ResumeHost(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := owner.Info(ctx); err != nil || info.Epoch != "restored" {
		t.Fatal(info, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := local.EmergencyStop(ctx); err != nil {
		t.Fatal("emergency stop requires a live kernel", err)
	}
}

func TestEmergencyStopPersistsAcrossHostRestart(t *testing.T) {
	root := t.TempDir()
	options := Options{StateDir: root, Factory: func() (computer.Provider, error) { return &desktop{epoch: "new"}, nil }}
	b, err := Start(options)
	if err != nil {
		t.Fatal(err)
	}
	local := computer.NewClient(b.Endpoint)
	if err := local.EmergencyStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = local.Close()
	_ = b.Close()
	b, err = Start(options)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	client := computer.NewClient(b.Endpoint)
	defer client.Close()
	if status, err := client.HostStatus(context.Background()); err != nil || !status.Suspended {
		t.Fatal("host restart resumed sharing", status, err)
	}
	if _, err := client.Info(context.Background()); err == nil {
		t.Fatal("restarted host accepted desktop access")
	}
	if err := client.ResumeHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Info(context.Background()); err != nil {
		t.Fatal("local resume failed", err)
	}
}
