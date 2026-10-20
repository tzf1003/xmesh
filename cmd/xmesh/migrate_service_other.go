//go:build !windows && !darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func readInstalledMigrationPaths(name string) (migrationPaths, error) {
	if runtime.GOOS != "linux" {
		return migrationPaths{}, fmt.Errorf("此平台请使用 --config 与 --state-dir 指定升级路径")
	}
	if err := migrationServiceName(name); err != nil {
		return migrationPaths{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return migrationPaths{}, err
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", name+".service"))
	if err != nil {
		return migrationPaths{}, err
	}
	section, command := "", ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
		}
		if section == "[Service]" && strings.HasPrefix(line, "ExecStart=") {
			if command != "" {
				return migrationPaths{}, fmt.Errorf("服务包含多个 ExecStart")
			}
			command = strings.TrimPrefix(line, "ExecStart=")
		}
	}
	args, ok := splitSetupUnitCommand(command)
	if !ok {
		return migrationPaths{}, fmt.Errorf("无效的服务命令")
	}
	return migrationPathsFromArgs(args)
}
