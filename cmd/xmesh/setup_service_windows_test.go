package main

import (
	"context"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/onboarding"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSetupWaitRetainsIdentityAfterEndpointRemoval(t *testing.T) {
	child := sleeper(t)
	dir := t.TempDir()
	ep := endpointOf(child.Process.Pid, time.Now().Add(-time.Second)).ControlEndpointFile
	ep.Transport = v1.TransportPipe
	ep.Endpoint = `\\.\pipe\setup-stop-test`
	if err := control.WriteEndpoint(dir, ep); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	wait, err := prepareSetupServiceStop(ctx, &onboarding.Plan{ConfigPath: filepath.Join(dir, "xmesh.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "control.json")); err != nil {
		t.Fatal(err)
	}
	if err = wait(); err == nil {
		t.Fatal("missing endpoint was accepted as process exit")
	}
	_ = child.Process.Kill()
	_, _ = child.Process.Wait()
}
