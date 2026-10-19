package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/privdir"
)

const computerServiceName = "xmesh-computer-host"

func nativeHelperPath(executable string) string {
	name := "xmesh-computer"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if runtime.GOOS == "darwin" {
		app := filepath.Join(filepath.Dir(executable), "xmesh-computer.app", "Contents", "MacOS", name)
		if st, err := os.Stat(app); err == nil && st.Mode().IsRegular() {
			return app
		}
	}
	return filepath.Join(filepath.Dir(executable), name)
}

func computerServiceConfig(executable, helper, state string) *service.Config {
	c := &service.Config{Name: computerServiceName, DisplayName: "XmeSh Computer", Description: "XmeSh 用户桌面服务", Executable: executable, Arguments: []string{"computer", "host", "--state-dir", state, "--helper", helper}, WorkingDirectory: filepath.Dir(executable), Option: service.KeyValue{"UserService": true, "RunAtLoad": true, "KeepAlive": true}}
	if runtime.GOOS == "linux" {
		c.Option["SystemdScript"] = computerSystemdScript
	}
	if runtime.GOOS == "darwin" {
		c.Option["LaunchdConfig"] = computerLaunchdConfig
	}
	return c
}

// The desktop host starts only as part of a graphical user session. In particular
// enabling lingering for CI runners must not create a background GUI authority.
const computerSystemdScript = `[Unit]
Description={{Description}}
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
Restart=on-failure
RestartSec=3
TimeoutStopSec=5

[Install]
WantedBy=graphical-session.target
`

const computerLaunchdConfig = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>{{Name | html}}</string>
<key>ProgramArguments</key><array><string>{{Path | html}}</string>{{range Arguments}}<string>{{. | html}}</string>{{end}}</array>
<key>LimitLoadToSessionType</key><string>Aqua</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>WorkingDirectory</key><string>{{WorkingDirectory | html}}</string>
</dict></plist>
`

func computerService(action string, args []string) error {
	f := flag.NewFlagSet("computer "+action, flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	helper := f.String("helper", "", "原生桌面执行器绝对路径")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("computer: unexpected arguments")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path := *helper
	if path == "" {
		path = nativeHelperPath(exe)
	}
	statePath, err := filepath.Abs(*state)
	if err != nil {
		return err
	}
	c := computerServiceConfig(exe, path, statePath)
	if action == "install" {
		if strings.Contains(filepath.ToSlash(exe), "/go-build") {
			return fmt.Errorf("请先将 xmesh 构建到持久路径后再安装桌面服务")
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("桌面执行器必须是绝对路径")
		}
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("原生桌面执行器不存在: %s", path)
		}
		if err = privdir.Ensure(statePath); err != nil {
			return err
		}
		if runtime.GOOS == "windows" {
			return installUserService(c)
		}
		s, err := service.New(&program{}, c)
		if err != nil {
			return err
		}
		if err = s.Install(); err != nil {
			return err
		}
		fmt.Println("已安装用户桌面服务:", computerServiceName)
		return nil
	}
	if runtime.GOOS == "windows" {
		return userTaskAction(action, c)
	}
	s, err := service.New(&program{}, c)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		err = s.Start()
	case "stop":
		err = s.Stop()
	case "restart":
		err = s.Restart()
	case "uninstall":
		_ = s.Stop()
		err = s.Uninstall()
	default:
		err = fmt.Errorf("未知桌面服务操作: %s", action)
	}
	return err
}
