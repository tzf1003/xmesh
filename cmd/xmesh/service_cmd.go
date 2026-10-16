package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// serviceName is the name install registers: xmesh-<node>.
func serviceName(node string) string { return "xmesh-" + node }

// serviceCommand implements uninstall, start, stop and restart for the service
// that install created. It resolves the node name from --config or --name.
func serviceCommand(action string, args []string) error {
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	path := flags.String("config", "xmesh.yaml", "配置路径（用来取得节点名）")
	name := flags.String("name", "", "服务名；省略时读取 --config 的节点名")
	system := flags.Bool("system", false, "Windows：操作管理员 SCM 服务")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if *system && runtime.GOOS != "windows" {
		return fmt.Errorf("--system 仅适用于 Windows")
	}
	serviceID := *name
	if serviceID == "" {
		cfg, e := config.Load(*path)
		if e != nil {
			return fmt.Errorf("无法读取配置以确定服务名（可改用 --name）: %w", e)
		}
		serviceID = serviceName(cfg.Node.Name)
	}
	c := &service.Config{Name: serviceID, DisplayName: "XmeSh", Option: service.KeyValue{"UserService": true, "RunAtLoad": true, "KeepAlive": true}}
	if runtime.GOOS == "windows" && !*system {
		if e := userTaskAction(action, c); e != nil {
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
			return e
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
		result["error"] = toolerr.New("internal", err.Error(), "op", "cli."+action).Object()
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
