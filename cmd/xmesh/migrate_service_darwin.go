package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func readInstalledMigrationPaths(name string) (migrationPaths, error) {
	if err := migrationServiceName(name); err != nil {
		return migrationPaths{}, err
	}
	label, err := resolveUserServiceName(name)
	if err != nil {
		return migrationPaths{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return migrationPaths{}, err
	}
	file := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if _, err := os.Stat(file); err != nil {
		return migrationPaths{}, err
	}
	raw, err := readLaunchAgent(file)
	if err != nil {
		return migrationPaths{}, err
	}
	var service struct {
		Label            string
		ProgramArguments []string
	}
	if err := json.Unmarshal(raw, &service); err != nil {
		return migrationPaths{}, err
	}
	if service.Label != label {
		return migrationPaths{}, fmt.Errorf("服务定义名称不匹配")
	}
	return migrationPathsFromArgs(service.ProgramArguments)
}
