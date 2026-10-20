package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// serviceName is the name install registers: xmesh-<node>.
func serviceName(node string) string { return "xmesh-" + node }

// notInstalled is the error for start, stop, restart and uninstall on a node whose service or
// logon task was never installed. The platform tools say so in the system language (schtasks
// may even answer in a legacy code page), so callers get a stable code instead of their text.
func notInstalled(serviceID string) *toolerr.Error {
	return toolerr.New("not_found", "服务未安装: "+serviceID+"（先运行 xmesh install）", "name", serviceID, "reason", "not_installed")
}

// serviceMissing reports whether err means the service does not exist.
func serviceMissing(err error) bool {
	return errors.Is(err, service.ErrNotInstalled) || platformServiceMissing(err)
}

// serviceCommand implements uninstall, start, stop and restart for the service
// that install created. It resolves the node name from --config or --name.
func serviceCommand(action string, args []string) error {
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	path := flags.String("config", "xmesh.yaml", "配置路径（用来取得节点名）")
	name := flags.String("name", "", "服务名；省略时读取 --config 的节点名")
	system := flags.Bool("system", false, "Windows：操作管理员 SCM 服务")
	destination := flags.String("relocate-to", "", "Windows，与 uninstall --dir 合用：将本用户登录任务迁移到新安装目录")
	dir := flags.String("dir", "", "Windows，仅 uninstall：删除本用户程序位于该目录下的登录任务，不动其他目录的节点")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if *system && runtime.GOOS != "windows" {
		return fmt.Errorf("--system 仅适用于 Windows")
	}
	if *destination != "" && *dir == "" {
		return fmt.Errorf("--relocate-to 需要 uninstall --dir")
	}
	if *dir != "" {
		if action != "uninstall" {
			return fmt.Errorf("--dir 仅用于 uninstall")
		}
		if *destination != "" {
			moved, e := relocateUserTasksUnder(*dir, *destination)
			if e != nil {
				return e
			}
			fmt.Printf("已迁移 %d 个登录任务到 %s\n", moved, *destination)
			return nil
		}
		removed, e := removeUserTasksUnder(*dir)
		if e != nil {
			return e
		}
		fmt.Printf("已删除 %d 个登录任务\n", removed)
		return nil
	}
	serviceID := *name
	if serviceID == "" {
		cfg, e := config.Load(*path)
		if e != nil {
			return fmt.Errorf("无法读取配置以确定服务名（可改用 --name）: %w", e)
		}
		serviceID = serviceName(cfg.Node.Name)
	}
	if !*system {
		var err error
		serviceID, err = resolveUserServiceName(serviceID)
		if err != nil {
			return err
		}
	}
	c := &service.Config{Name: serviceID, DisplayName: "XmeSh", Option: service.KeyValue{"UserService": true, "RunAtLoad": true, "KeepAlive": true}}
	if runtime.GOOS == "windows" && !*system {
		if e := userTaskAction(action, c); e != nil {
			if serviceMissing(e) {
				return notInstalled(serviceID)
			}
			return e
		}
	} else {
		s, e := service.New(&program{}, c)
		if e != nil {
			return e
		}
		switch action {
		case "uninstall":
			e = s.Uninstall()
		case "start":
			e = s.Start()
		case "stop":
			e = s.Stop()
		case "restart":
			e = s.Restart()
		}
		if e != nil {
			if _, status := s.Status(); serviceMissing(e) || serviceMissing(status) {
				return notInstalled(serviceID)
			}
			return e
		}
		if runtime.GOOS == "darwin" && (action == "start" || action == "restart") {
			// A manually started LaunchAgent may have RunAtLoad disabled.
			if e = exec.Command("launchctl", "start", serviceID).Run(); e != nil {
				return e
			}
		}
	}
	fmt.Printf("已%s服务: %s\n", map[string]string{"uninstall": "卸载", "start": "启动", "stop": "停止", "restart": "重启"}[action], serviceID)
	return nil
}

// withJSON runs a subcommand and, when --json is among its arguments, replaces
// its human output with one JSON object: {ok, action, messages} on success and
// {ok:false, action, error:{error,message}} on failure. The error is still
// returned, so the exit status stays non-zero.
func withJSON(action string, args []string, run func([]string) error) error {
	rest, machine := stripFlag(args, "json")
	if !machine {
		return run(args)
	}
	captured, err := captureStdout(func() error { return run(rest) })
	result := map[string]any{"ok": err == nil, "action": action}
	if err != nil {
		var typed *toolerr.Error
		if errors.As(err, &typed) {
			result["error"] = typed.Object()
		} else {
			result["error"] = toolerr.New("internal", err.Error(), "op", "cli."+action).Object()
		}
	}
	messages := []string{}
	for _, line := range strings.Split(strings.TrimSpace(captured), "\n") {
		if line != "" {
			messages = append(messages, line)
		}
	}
	result["messages"] = messages
	raw, _ := json.Marshal(result)
	fmt.Println(string(raw))
	return err
}

// stripFlag removes a boolean --name / --name=true flag, reporting whether it was set.
func stripFlag(args []string, name string) ([]string, bool) {
	out, set := make([]string, 0, len(args)), false
	for _, a := range args {
		switch a {
		case "--" + name, "-" + name, "--" + name + "=true":
			set = true
		default:
			out = append(out, a)
		}
	}
	return out, set
}

func captureStdout(run func() error) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	saved := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, r); close(done) }()
	runErr := run()
	os.Stdout = saved
	_ = w.Close()
	<-done
	_ = r.Close()
	return buf.String(), runErr
}
