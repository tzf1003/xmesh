package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/computer/adengine"
	"github.com/tzf1003/xmesh/internal/desktopbroker"
)

func computerCommand(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "install", "start", "stop", "restart", "uninstall", "setup":
			return withJSON("computer."+args[0], args[1:], func(rest []string) error { return computerService(args[0], rest) })
		}
	}
	if len(args) > 0 && args[0] == "engine" {
		return computerEngine(args[1:])
	}
	if len(args) == 0 || args[0] == "help" {
		fmt.Println("用法: xmesh computer host [--state-dir 目录] [--helper 执行器绝对路径] [--engine native|agent-desktop] [--agent-desktop 绝对路径] [--authorize]\n      xmesh computer setup [--endpoint 本机套接字或管道] [--json]\n      xmesh computer install|start|stop|restart|uninstall [--json]\n      xmesh computer engine [native|agent-desktop] [--state-dir 目录]（读取或切换本设备桌面引擎，需重启 Host 生效）\n      xmesh computer engine status [--state-dir 目录]（本机引擎版本、验收与更新状态）\n      xmesh computer engine update [--to 版本] [--state-dir 目录]（安装清单钉住的版本并重启复核，失败回滚）\n      xmesh computer engine rollback [--state-dir 目录]（切回上一版本）\n      xmesh computer status|info|authorize|emergency-stop|resume [--endpoint 本机套接字或管道]")
		return nil
	}
	f := flag.NewFlagSet("computer "+args[0], flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	helper := f.String("helper", "", "原生桌面执行器的绝对路径")
	engineFlag := f.String("engine", "", "桌面引擎：native 或 agent-desktop；缺省读取状态目录的 engine 文件，再缺省为 native")
	adHelper := f.String("agent-desktop", "", "agent-desktop 可执行文件的绝对路径")
	authorize := f.Bool("authorize", false, "在本机请求系统桌面权限")
	endpoint := f.String("endpoint", "", "本机桌面服务端点")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("computer: unexpected arguments")
	}
	if *endpoint == "" {
		*endpoint = computer.EndpointForState(*state)
	}
	switch args[0] {
	case "emergency-stop", "resume":
		c := computer.NewClient(*endpoint)
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		if args[0] == "emergency-stop" {
			err = c.EmergencyStop(ctx)
		} else {
			err = c.ResumeHost(ctx)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"suspended": args[0] == "emergency-stop"})
	case "host":
		path := *helper
		if path == "" {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			path = nativeHelperPath(exe)
		}
		engine, err := computer.ParseEngine(*engineFlag)
		if err == nil && *engineFlag == "" {
			engine, err = computer.ReadEngine(*state)
		}
		if err != nil {
			return err
		}
		statePath, err := filepath.Abs(*state)
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if engine == computer.EngineAgentDesktop {
			// A switch made by the automatic path is checked before anything is served.
			pendingCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			change, perr := computer.CheckPendingEngine(pendingCtx, statePath)
			cancel()
			if perr != nil {
				slog.Warn("agent_desktop_pending_check", "error", perr)
			} else if change.RolledBack {
				slog.Warn("agent_desktop_rolled_back", "version", change.To, "reason", change.Reason)
			}
		}
		opts := computer.EngineOptions{Engine: engine, NativePath: path, AgentDesktopPath: *adHelper, BundledPath: computer.AgentDesktopHelperPath(exe), StateDir: statePath}
		if res, rerr := opts.Resolve(); rerr == nil && res.Effective != res.Configured {
			slog.Warn("desktop_engine_fallback", "configured", res.Configured, "effective", res.Effective, "state", res.Status.State, "reason", res.Status.Reason)
		}
		normal, authorized, err := opts.Factories()
		if err != nil {
			return err
		}
		b, err := desktopbroker.Start(desktopbroker.Options{StateDir: *state, HelperPath: path, Authorize: *authorize, Version: buildVersion, Factory: normal, AuthorizedFactory: authorized})
		if err != nil {
			return err
		}
		defer b.Close()
		if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"endpoint": b.Endpoint, "protocol_version": computer.ProtocolVersion}); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		<-ctx.Done()
		return nil
	case "status":
		c := computer.NewClient(*endpoint)
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		v, err := c.HostStatus(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "info":
		c := computer.NewClient(*endpoint)
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		v, err := c.Info(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "authorize":
		c := computer.NewClient(*endpoint)
		// This client does not acquire the kernel binding, so it must not disconnect
		// an existing owner after requesting the local system permission prompt.
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Authorize(ctx); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"requested": true})
	default:
		return fmt.Errorf("未知 computer 子命令: %s", args[0])
	}
}

// computerEngine reads or sets the per-device engine. The choice is a file in the
// Host state directory so switching (and rolling back) needs no service reinstall.
func computerEngine(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return computerEngineStatus(args[1:])
		case "update":
			return computerEngineUpdate(args[1:])
		case "rollback":
			return computerEngineRollback(args[1:])
		}
	}
	f := flag.NewFlagSet("computer engine", flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return fmt.Errorf("computer engine: unexpected arguments")
	}
	if f.NArg() == 1 {
		e, err := computer.ParseEngine(f.Arg(0))
		if err != nil {
			return err
		}
		if err = os.MkdirAll(*state, 0o700); err != nil {
			return err
		}
		if err = computer.WriteEngine(*state, e); err != nil {
			return err
		}
	}
	e, err := computer.ReadEngine(*state)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"engine": e, "restart_required": f.NArg() == 1})
}

// computerEngineStatus reports the agent-desktop engine on this device without
// starting the desktop executor. Everything it prints is local; the Host's Info
// carries the same engine fields to clients.
func computerEngineStatus(args []string) error {
	f := flag.NewFlagSet("computer engine status", flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("computer engine status: unexpected arguments")
	}
	statePath, err := filepath.Abs(*state)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	report, err := computer.ReportEngine(statePath, computer.AgentDesktopHelperPath(exe))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}

// computerEngineUpdate installs the release the manifest pins, restarts the
// desktop Host, and checks that the restarted Host runs it. A failed check rolls
// the switch back and restarts the previous engine.
func computerEngineUpdate(args []string) error {
	f := flag.NewFlagSet("computer engine update", flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	to := f.String("to", "", "目标版本；缺省且唯一可用的是清单钉住的版本")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("computer engine update: unexpected arguments")
	}
	statePath, err := filepath.Abs(*state)
	if err != nil {
		return err
	}
	u, err := computer.NewEngineUpdater(statePath)
	if err != nil {
		return err
	}
	if *to != "" && *to != u.Manifest.Version {
		return fmt.Errorf("清单只钉住 %s，不能安装 %s", u.Manifest.Version, *to)
	}
	// A native Host never loads the release, so a restart-and-verify here would
	// always fail and roll back. Select agent-desktop first.
	conf, err := computer.ReadEngine(statePath)
	if err != nil {
		return err
	}
	if conf != computer.EngineAgentDesktop {
		return fmt.Errorf("本机引擎为 %s；先执行 xmesh computer engine agent-desktop，再更新", conf)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	change, err := u.Apply(ctx, adengine.Hooks{
		Restart: func(context.Context) error { return restartComputerHost(statePath) },
		Verify:  func(ctx context.Context) error { return verifyComputerEngine(ctx, statePath, u.Manifest.Version) },
	})
	if encErr := json.NewEncoder(os.Stdout).Encode(map[string]any{"change": change}); encErr != nil && err == nil {
		err = encErr
	}
	return err
}

// computerEngineRollback switches back to the previously active engine.
func computerEngineRollback(args []string) error {
	f := flag.NewFlagSet("computer engine rollback", flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("computer engine rollback: unexpected arguments")
	}
	statePath, err := filepath.Abs(*state)
	if err != nil {
		return err
	}
	u, err := computer.NewEngineUpdater(statePath)
	if err != nil {
		return err
	}
	conf, err := computer.ReadEngine(statePath)
	if err != nil {
		return err
	}
	if conf != computer.EngineAgentDesktop {
		return fmt.Errorf("本机引擎为 %s，无需回滚 agent-desktop", conf)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	change, err := u.Rollback(ctx, adengine.Hooks{Restart: func(context.Context) error { return restartComputerHost(statePath) }})
	if encErr := json.NewEncoder(os.Stdout).Encode(map[string]any{"change": change}); encErr != nil && err == nil {
		err = encErr
	}
	return err
}

// restartComputerHost restarts the user's desktop service. When no Host is
// answering, or the Host is not a managed service, there is nothing to restart
// from here: the new engine applies at the next Host start.
func restartComputerHost(statePath string) error {
	client := computer.NewClient(computer.EndpointForState(statePath))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.HostStatus(ctx); err != nil {
		return fmt.Errorf("%w: %v", adengine.ErrRestartUnavailable, err)
	}
	err := computerService("restart", []string{"--state-dir", statePath})
	if err != nil && serviceMissing(err) {
		return fmt.Errorf("%w: %v", adengine.ErrRestartUnavailable, err)
	}
	return err
}

// verifyComputerEngine waits for the restarted Host to report the new engine as
// running and usable. The check uses the Host's own Info, so it covers the same
// verification the Host performed at startup.
func verifyComputerEngine(ctx context.Context, statePath, version string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := fetchComputerInfo(ctx, statePath)
		if err == nil && info.Engine == string(computer.EngineAgentDesktop) && info.EngineVersion == version && adengine.Usable(info.EngineStatus) {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("engine=%s status=%s reason=%s version=%s", info.Engine, info.EngineStatus, info.EngineReason, info.EngineVersion)
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func fetchComputerInfo(ctx context.Context, statePath string) (computer.Info, error) {
	client := computer.NewClient(computer.EndpointForState(statePath))
	defer client.Close()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return client.Info(cctx)
}
