package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	if len(args) == 0 || args[0] == "help" {
		fmt.Println("用法: xmesh computer host [--state-dir 目录] [--helper 执行器绝对路径] [--authorize]\n      xmesh computer setup|install|start|stop|restart|uninstall [--json]\n      xmesh computer status|info|authorize|emergency-stop|resume [--endpoint 本机套接字或管道]")
		return nil
	}
	f := flag.NewFlagSet("computer "+args[0], flag.ContinueOnError)
	state := f.String("state-dir", computer.DefaultStateDir(), "用户桌面服务状态目录")
	helper := f.String("helper", "", "原生桌面执行器的绝对路径")
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
		b, err := desktopbroker.Start(desktopbroker.Options{StateDir: *state, HelperPath: path, Authorize: *authorize, Version: buildVersion})
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
