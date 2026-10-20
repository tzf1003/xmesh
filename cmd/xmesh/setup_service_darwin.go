package main

import (
	"context"
	"encoding/json"
	"errors"
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
	name, e := resolveUserServiceName(serviceName(p.Input.NodeName))
	if e != nil {
		return snap, e
	}
	snap.Path = filepath.Join(home, "Library", "LaunchAgents", name+".plist")
	snap.Definition, e = os.ReadFile(snap.Path)
	if os.IsNotExist(e) {
		return snap, nil
	}
	if e != nil {
		return snap, e
	}
	snap.Installed = true
	raw, e := readLaunchAgent(snap.Path)
	if e != nil {
		return snap, e
	}
	var registration struct {
		Label            string
		ProgramArguments []string
	}
	if e = json.Unmarshal(raw, &registration); e != nil {
		return snap, e
	}
	if registration.Label != name || len(registration.ProgramArguments) < 4 || registration.ProgramArguments[1] != "serve" {
		return snap, errors.New("service registration differs")
	}
	match := false
	for i := 2; i+1 < len(registration.ProgramArguments); i++ {
		if registration.ProgramArguments[i] == "--config" && filepath.Clean(registration.ProgramArguments[i+1]) == filepath.Clean(p.ConfigPath) {
			match = true
		}
	}
	if !match {
		return snap, &onboarding.Error{Code: "service_conflict", Message: "同名服务使用另一个配置，请选择新的设备名", Field: "node_name"}
	}
	out, e := exec.CommandContext(ctx, "launchctl", "list", name).Output()
	snap.Loaded, snap.Running = setupLaunchAgentState(out, e)
	return snap, nil
}

func restoreSetupService(ctx context.Context, p *onboarding.Plan, snap onboarding.ServiceSnapshot) error {
	if !snap.Installed {
		return nil
	}
	name := strings.TrimSuffix(filepath.Base(snap.Path), ".plist")
	_ = exec.CommandContext(ctx, "launchctl", "unload", snap.Path).Run()
	if e := os.WriteFile(snap.Path, snap.Definition, 0600); e != nil {
		return e
	}
	return restoreSetupLaunchAgent(ctx, name, snap, func(ctx context.Context, args ...string) error {
		return exec.CommandContext(ctx, "launchctl", args...).Run()
	})
}

func setupLaunchAgentState(output []byte, err error) (loaded, running bool) {
	return err == nil, err == nil && strings.Contains(string(output), `"PID" =`)
}

func restoreSetupLaunchAgent(ctx context.Context, name string, snap onboarding.ServiceSnapshot, run func(context.Context, ...string) error) error {
	// Running implies loaded for journals written by older versions.
	if snap.Loaded || snap.Running {
		if err := run(ctx, "load", snap.Path); err != nil {
			return err
		}
	}
	if snap.Running {
		return run(ctx, "start", name)
	}
	return nil
}
