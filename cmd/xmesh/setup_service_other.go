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

func prepareSetupServiceStop(context.Context, *onboarding.Plan) (func() error, error) {
	return func() error { return nil }, nil
}

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
	if !setupUnitConfigMatches(snap.Definition, p.ConfigPath) {
		return snap, &onboarding.Error{Code: "service_conflict", Message: "同名服务使用另一个配置，请选择新的设备名", Field: "node_name"}
	}
	manager, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=FragmentPath,DropInPaths,NeedDaemonReload", serviceName(p.Input.NodeName)).Output()
	if err != nil || !setupUnitManagerMatches(manager, snap.Path) {
		return snap, &onboarding.Error{Code: "service_conflict", Message: "服务包含覆盖配置或待加载改动，请检查用户服务后重试", Field: "node_name"}
	}
	snap.Running = exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", serviceName(p.Input.NodeName)).Run() == nil
	enabled, err := exec.CommandContext(ctx, "systemctl", "--user", "is-enabled", serviceName(p.Input.NodeName)).Output()
	snap.Enabled = strings.TrimSpace(string(enabled))
	if err != nil && snap.Enabled == "" {
		return snap, err
	}
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
	if snap.Enabled != "" {
		// Clear enablement created by the attempted replacement before restoring
		// an originally runtime-only or disabled registration.
		if e := exec.CommandContext(ctx, "systemctl", "--user", "disable", serviceName(p.Input.NodeName)).Run(); e != nil {
			return e
		}
	}
	if args := systemdRestoreEnablement(snap.Enabled, serviceName(p.Input.NodeName)); args != nil {
		if e := exec.CommandContext(ctx, "systemctl", args...).Run(); e != nil {
			return e
		}
	}
	action := "stop"
	if snap.Running {
		action = "restart"
	}
	return exec.CommandContext(ctx, "systemctl", "--user", action, serviceName(p.Input.NodeName)).Run()
}

func systemdRestoreEnablement(state, name string) []string {
	switch state {
	case "enabled":
		return []string{"--user", "enable", name}
	case "enabled-runtime":
		return []string{"--user", "enable", "--runtime", name}
	case "disabled", "static", "indirect":
		return []string{"--user", "disable", name}
	default:
		return nil
	}
}
