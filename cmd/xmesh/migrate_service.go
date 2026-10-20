package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type migrationPaths struct {
	config, state, password string
	localOnly, system       bool
}

var installedMigrationPaths = readInstalledMigrationPaths

func migrationServiceName(name string) error {
	if !strings.HasPrefix(name, "xmesh-") || len(name) > 69 {
		return fmt.Errorf("无效的 xmesh 服务名")
	}
	for _, r := range strings.TrimPrefix(name, "xmesh-") {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("无效的 xmesh 服务名")
		}
	}
	if name == "xmesh-" {
		return fmt.Errorf("无效的 xmesh 服务名")
	}
	return nil
}

func migrationPathsFromArgs(args []string) (migrationPaths, error) {
	var paths migrationPaths
	if len(args) < 2 || args[1] != "serve" {
		return paths, fmt.Errorf("服务未运行 xmesh serve")
	}
	for i := 2; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag == "--local-only" {
			paths.localOnly = !inline || value == "true"
			continue
		}
		if flag != "--config" && flag != "--state-dir" && flag != "--oauth-password-file" {
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				return migrationPaths{}, fmt.Errorf("服务参数 %s 缺少路径", flag)
			}
			i++
			value = args[i]
		}
		if value == "" {
			return migrationPaths{}, fmt.Errorf("服务参数 %s 缺少路径", flag)
		}
		if flag == "--config" {
			paths.config = value
		} else if flag == "--state-dir" {
			paths.state = value
		} else {
			paths.password = value
		}
	}
	if !filepath.IsAbs(paths.config) || paths.state != "" && !filepath.IsAbs(paths.state) || paths.password != "" && !filepath.IsAbs(paths.password) {
		return migrationPaths{}, fmt.Errorf("服务配置路径必须为绝对路径")
	}
	return paths, nil
}

func migrationTaskCommand(raw []byte) (string, string, error) {
	var task userTask
	decoder := xml.NewDecoder(strings.NewReader(taskDocumentText(raw)))
	decoder.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := decoder.Decode(&task); err != nil {
		return "", "", err
	}
	return task.Actions.Exec.Command, task.Actions.Exec.Arguments, nil
}
