//go:build !windows && !darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tzf1003/xmesh/internal/onboarding"
)

func waitSetupServiceStopped(context.Context, *onboarding.Plan) error { return nil }

func snapshotSetupService(ctx context.Context, p *onboarding.Plan) (onboarding.ServiceSnapshot, error) {
	var snap onboarding.ServiceSnapshot
	home, e := os.UserHomeDir()
	if e != nil {
		return snap, e
	}
	snap.Path = filepath.Join(home, ".config", "systemd", "user", serviceName(p.Input.NodeName)+".service")
	snap.Definition, e = os.ReadFile(snap.Path)
	if os.IsNotExist(e) {
		return snap, nil
	}
	if e != nil {
		return snap, e
	}
	snap.Installed = true
	if !strings.Contains(string(snap.Definition), p.ConfigPath) {
		return snap, &onboarding.Error{Code: "service_conflict", Message: "同名服务使用另一个配置，请选择新的设备名", Field: "node_name"}
	}
	snap.Running = exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", serviceName(p.Input.NodeName)).Run() == nil
	return snap, nil
}
func restoreSetupService(ctx context.Context, p *onboarding.Plan, snap onboarding.ServiceSnapshot) error {
	if !snap.Installed {
		return nil
	}
	if e := os.WriteFile(snap.Path, snap.Definition, 0600); e != nil {
		return e
	}
	if e := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run(); e != nil {
		return e
	}
	action := "stop"
	if snap.Running {
		action = "restart"
	}
	return exec.CommandContext(ctx, "systemctl", "--user", action, serviceName(p.Input.NodeName)).Run()
}
