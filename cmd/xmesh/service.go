package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/privdir"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

type program struct {
	store     *config.Store
	options   app.Options
	cancel    context.CancelFunc
	done      chan struct{}
	err       error
	run       func(context.Context, *config.Store, app.Options) error
	onFailure func(error)
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	run := p.run
	if run == nil {
		run = app.Run
	}
	go func() {
		err := run(ctx, p.store, p.options)
		failed := ctx.Err() == nil
		if failed && err == nil {
			err = fmt.Errorf("服务运行意外结束")
		}
		p.err = err
		close(p.done)
		if failed && p.onFailure != nil {
			p.onFailure(err)
		}
	}()
	return nil
}
func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done == nil {
		return nil
	}
	select {
	case <-p.done:
		return p.err
	case <-time.After(p.stopLimit()):
		return fmt.Errorf("等待服务停止超时")
	}
}

// stopLimit is how long Stop waits for the runtime. A node with the Windows
// AppContainer runner also waits for the runner to reclaim live sandboxes, which
// is bounded by app.RunnerShutdownLimit after the roles have drained.
func (p *program) stopLimit() time.Duration {
	limit := 30 * time.Second
	if runtime.GOOS == "windows" && p.store != nil && p.store.Get().Agent.Policy.WindowsAppContainer {
		limit += app.RunnerShutdownLimit()
	}
	return limit
}
func runService(store *config.Store, options app.Options) error {
	p := &program{store: store, options: options, onFailure: func(e error) {
		fmt.Fprintln(os.Stderr, "服务运行失败:", e)
		os.Exit(1)
	}}
	s, e := service.New(p, &service.Config{Name: "xmesh-" + store.Get().Node.Name, DisplayName: "XmeSh", Option: service.KeyValue{"UserService": true}})
	if e != nil {
		return e
	}
	return s.Run()
}
func install(args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	path := flags.String("config", "xmesh.yaml", "配置路径")
	executable := flags.String("executable", "", "已构建的 xmesh 可执行文件")
	state := flags.String("state-dir", "", "状态目录")
	password := flags.String("oauth-password-file", "", "OAuth 口令文件")
	system := flags.Bool("system", false, "Windows：安装为管理员 SCM 服务")
	localOnly := flags.Bool("local-only", false, "仅通过本机 Gateway 提供工具")
	autostart := flags.Bool("autostart", true, "登录后自动启动")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if *system && runtime.GOOS != "windows" {
		return fmt.Errorf("--system 仅适用于 Windows")
	}
	absolute, e := filepath.Abs(*path)
	if e != nil {
		return e
	}
	cfg, e := config.Load(absolute)
	if e != nil {
		return e
	}
	exe := *executable
	if exe == "" {
		exe, e = os.Executable()
		if e != nil {
			return e
		}
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		return e
	}
	st, e := os.Stat(exe)
	if e != nil || st.IsDir() {
		return fmt.Errorf("可执行文件不可用")
	}
	if strings.Contains(filepath.ToSlash(exe), "/go-build") {
		return fmt.Errorf("请先构建持久路径中的 xmesh 二进制，再执行 install")
	}
	arguments := []string{"serve"}
	if *localOnly {
		arguments = append(arguments, "--local-only")
	}
	if runtime.GOOS != "windows" || *system {
		arguments = append(arguments, "--service")
	}
	arguments = append(arguments, "--config", absolute)
	if *system {
		// The service runs as SYSTEM; let the installing user's UI reach its control pipe.
		if sid := installerSID(); sid != "" {
			arguments = append(arguments, "--control-allow-sid", sid)
		}
	}
	if dir := installStateDir(runtime.GOOS, os.Getenv, *system, *state, cfg.Node.Name); dir != "" {
		p, e := filepath.Abs(dir)
		if e != nil {
			return e
		}
		arguments = append(arguments, "--state-dir", p)
	}
	if *password != "" {
		p, e := filepath.Abs(*password)
		if e != nil {
			return e
		}
		arguments = append(arguments, "--oauth-password-file", p)
	}
	serviceConfig := &service.Config{Name: "xmesh-" + cfg.Node.Name, DisplayName: "XmeSh " + cfg.Node.Name, Description: "XmeSh 设备 MCP 服务", Executable: exe, Arguments: arguments, WorkingDirectory: filepath.Dir(absolute), Option: service.KeyValue{"UserService": true, "RunAtLoad": *autostart, "KeepAlive": *autostart}}
	if runtime.GOOS == "windows" && !*system {
		return installUserService(serviceConfig)
	}
	s, e := service.New(&program{}, serviceConfig)
	if e != nil {
		return e
	}
	if e = s.Install(); e != nil {
		return e
	}
	if runtime.GOOS == "linux" && !*autostart {
		if e = exec.Command("systemctl", "--user", "disable", serviceConfig.Name).Run(); e != nil {
			return e
		}
	}
	fmt.Println("已安装服务:", serviceConfig.Name)
	return nil
}

// installStateDir is the --state-dir an installed service is given. A SYSTEM
// service on Windows would otherwise keep its state under the service
// profile, which the user's desktop UI cannot read, so it defaults to
// %ProgramData%\xmesh\<node>, the place the UI looks for such nodes.
func installStateDir(goos string, getenv func(string) string, system bool, flagValue, node string) string {
	if flagValue != "" || !system {
		return flagValue
	}
	root := control.SystemStateRootFor(goos, getenv)
	if root == "" {
		return ""
	}
	return root + `\` + node
}
func pair(args []string) error {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	path := flags.String("config", "xmesh.yaml", "配置路径")
	state := flags.String("state-dir", "", "状态目录")
	export := flags.String("export", "", "导出本节点公开配对文件")
	importFile := flags.String("import", "", "导入对端公开配对文件")
	fingerprint := flags.String("fingerprint", "", "独立核对的对端 SHA-256 指纹")
	tags := flags.String("tags", "tag:peer,tag:device", "授予对端的 LAN 角色")
	if e := flags.Parse(args); e != nil {
		return e
	}
	cfg, e := config.Load(*path)
	if e != nil {
		return e
	}
	if cfg.Mesh.Mode != "lan" {
		return fmt.Errorf("pair 仅用于 lan 模式")
	}
	dir := *state
	if dir == "" {
		dir, e = app.StateDir(cfg.Node.Name)
		if e != nil {
			return e
		}
	}
	if e = privdir.Ensure(dir); e != nil {
		return e
	}
	dir = filepath.Join(dir, "lan")
	if (*export == "") == (*importFile == "") {
		return fmt.Errorf("请选择 --export 或 --import")
	}
	if *export != "" {
		fp, e := mesh.ExportPair(dir, cfg.Node.Name, *export)
		if e != nil {
			return e
		}
		fmt.Println("节点证书指纹:", fp)
		return nil
	}
	granted := strings.Split(*tags, ",")
	if e = mesh.ImportPair(dir, *importFile, *fingerprint, granted); e != nil {
		return e
	}
	fmt.Println("已保存对端配对证书；重启节点后生效")
	if note := missingDeviceTagNote(granted); note != "" {
		fmt.Println(note)
	}
	return nil
}

// missingDeviceTagNote says what a pairing without tag:device means. LAN discovery lists only
// peers that carry tag:device as devices; the others show up as online and nothing more.
func missingDeviceTagNote(tags []string) string {
	if slices.Contains(tags, "tag:device") {
		return ""
	}
	return "注意：未授予 tag:device 的对端只显示在线状态，不会出现在设备列表。需要它作为设备时，请加上 tag:device 后重新导入。"
}
