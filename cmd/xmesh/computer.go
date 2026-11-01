package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
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
		fmt.Println("用法: xmesh computer host [--state-dir 目录] [--helper 执行器绝对路径] [--engine native|agent-desktop] [--agent-desktop 绝对路径] [--authorize]\n      xmesh computer setup [--endpoint 本机套接字或管道] [--json]\n      xmesh computer install|start|stop|restart|uninstall [--json]\n      xmesh computer engine [native|agent-desktop] [--state-dir 目录]（读取或切换本设备桌面引擎，需重启 Host 生效）\n      xmesh computer status|info|authorize|emergency-stop|resume [--endpoint 本机套接字或管道]")
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
		ad := *adHelper
		if ad == "" && engine == computer.EngineAgentDesktop {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			ad = computer.AgentDesktopHelperPath(exe)
		}
		statePath, err := filepath.Abs(*state)
		if err != nil {
			return err
		}
		normal, authorized, err := computer.EngineOptions{Engine: engine, NativePath: path, AgentDesktopPath: ad, StateDir: statePath}.Factories()
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
