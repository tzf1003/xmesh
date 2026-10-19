package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

// A node can have been renamed since its LaunchAgent was installed. Resolve its installed
// label from the service's configuration before asking launchd to stop or start it.
func resolveUserServiceName(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return findUserServiceName(filepath.Join(home, "Library", "LaunchAgents"), name, readLaunchAgent)
}

func readLaunchAgent(file string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", file).Output()
}

func findUserServiceName(dir, name string, readPlist func(string) ([]byte, error)) (string, error) {
	if !strings.HasPrefix(name, "xmesh-") {
		return name, nil
	}
	if _, err := os.Stat(filepath.Join(dir, name+".plist")); err == nil {
		return name, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return name, nil
		}
		return "", err
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "xmesh-") || !strings.HasSuffix(entry.Name(), ".plist") {
			continue
		}
		raw, err := readPlist(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var service struct {
			Label            string
			ProgramArguments []string
		}
		if json.Unmarshal(raw, &service) != nil || service.Label+".plist" != entry.Name() {
			continue
		}
		args := service.ProgramArguments
		if len(args) < 4 || args[1] != "serve" {
			continue
		}
		var file string
		for i := 2; i+1 < len(args); i++ {
			if args[i] == "--config" {
				file = args[i+1]
				break
			}
		}
		if !filepath.IsAbs(file) {
			continue
		}
		cfg, err := config.Load(file)
		if err == nil && serviceName(cfg.Node.Name) == name {
			matches = append(matches, service.Label)
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("多个服务使用这个节点名，请用 --name 指定已安装的服务名: %s", strings.Join(matches, "、"))
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return name, nil
}
