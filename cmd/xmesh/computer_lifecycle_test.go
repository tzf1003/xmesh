package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/computer"
)

// Opt in only on a disposable user session that has no desktop host installed.
// The native child is observed but this test does not inject desktop input.
func TestComputerInstalledHostLifecycle(t *testing.T) {
	binary, helper := os.Getenv("XMESH_TEST_COMPUTER_SERVICE_BINARY"), os.Getenv("XMESH_TEST_COMPUTER_HELPER")
	if binary == "" || helper == "" {
		t.Skip("requires built binaries and an isolated user service installation")
	}
	if !filepath.IsAbs(binary) || !filepath.IsAbs(helper) {
		t.Fatal("absolute test binary paths required")
	}
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		// Darwin's test-name-prefixed TMPDIR can exceed sockaddr_un.sun_path.
		var err error
		root, err = os.MkdirTemp("/tmp", "xmesh-host-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	}
	config := computerServiceConfig(binary, helper, root)
	if runtime.GOOS == "windows" {
		if err := userTaskAction("probe", config); !serviceMissing(err) {
			t.Fatal("lifecycle test requires an absent user desktop task", err)
		}
	} else {
		controller, err := service.New(&program{}, config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Status(); !serviceMissing(err) {
			t.Fatal("lifecycle test requires an absent user desktop service", err)
		}
	}
	run := func(action string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, "computer", action, "--state-dir", root, "--helper", helper, "--json").CombinedOutput()
		if err != nil {
			t.Log(action, string(out))
			return err
		}
		var result struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(out, &result); err != nil || !result.OK {
			t.Fatalf("%s returned invalid result: %s", action, out)
		}
		return nil
	}
	if err := run("install"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run("uninstall"); err != nil {
			t.Error("desktop service cleanup", err)
		}
	})
	client := computer.NewClient(computer.EndpointForState(root))
	defer client.Close()
	wait := func(running bool) computer.HostStatus {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for {
			state, err := client.HostStatus(ctx)
			if err == nil && state.Running == running {
				return state
			}
			if ctx.Err() != nil {
				t.Fatal("desktop lifecycle state", state, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err := run("start"); err != nil {
		t.Fatal(err)
	}
	first := wait(true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	before, err := client.Info(ctx)
	if err != nil || before.Epoch == "" {
		t.Fatal("native desktop identity", before, err)
	}
	active := wait(true)
	if active.NativePID == 0 {
		t.Fatal("native child did not start", active)
	}
	if err := run("restart"); err != nil {
		t.Fatal(err)
	}
	after := wait(true)
	if first.PID == after.PID {
		t.Fatal("host restart retained old process", first, after)
	}
	next, err := client.Info(ctx)
	if err != nil || next.Epoch == "" || next.Epoch == before.Epoch {
		t.Fatal("restart reused desktop epoch", next, err)
	}
	local := computer.NewClient(computer.EndpointForState(root))
	defer local.Close()
	if err := local.EmergencyStop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Info(ctx); err == nil {
		t.Fatal("emergency stopped desktop remained reachable")
	}
	if err := run("restart"); err != nil {
		t.Fatal(err)
	}
	wait(true)
	if status, err := local.HostStatus(ctx); err != nil || !status.Suspended {
		t.Fatal("restarting service resumed an emergency stopped desktop", status, err)
	}
	if err := local.ResumeHost(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Info(ctx); err != nil {
		t.Fatal("explicit local resume", err)
	}
	if err := run("stop"); err != nil {
		t.Fatal(err)
	}
	wait(false)
	t.Log("installed user host supervised native input, restarted with a new epoch, preserved emergency suspension until local resume, and stopped")
}
