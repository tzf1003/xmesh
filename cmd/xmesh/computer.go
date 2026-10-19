package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/desktopbroker"
)

func computerCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Println("用法: xmesh computer host [--state-dir 目录] [--helper 执行器绝对路径]\n      xmesh computer info [--endpoint 本机套接字或管道]")
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
	switch args[0] {
	case "host":
		path := *helper
		if path == "" {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			name := "xmesh-computer"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			path = filepath.Join(filepath.Dir(exe), name)
			if runtime.GOOS == "darwin" {
				app := filepath.Join(filepath.Dir(exe), "xmesh-computer.app", "Contents", "MacOS", "xmesh-computer")
				if _, err = os.Stat(app); err == nil {
					path = app
				}
			}
		}
		b, err := desktopbroker.Start(desktopbroker.Options{StateDir: *state, HelperPath: path, Authorize: *authorize})
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
	default:
		return fmt.Errorf("未知 computer 子命令: %s", args[0])
	}
}
