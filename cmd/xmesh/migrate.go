package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

func migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	file := flags.String("config", "xmesh.yaml", "待升级配置")
	state := flags.String("state-dir", "", "节点状态目录")
	name := flags.String("name", "", "读取已安装服务的实际配置路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("migrate 不接受位置参数")
	}
	if *name != "" {
		conflict := false
		flags.Visit(func(f *flag.Flag) { conflict = conflict || f.Name == "config" || f.Name == "state-dir" })
		if conflict {
			return fmt.Errorf("--name 与 --config/--state-dir 不能同时使用")
		}
		paths, err := installedMigrationPaths(*name)
		if err != nil {
			return err
		}
		*file, *state = paths.config, paths.state
	}
	path, err := filepath.Abs(*file)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return err
	}
	next, err := config.MigrateDeviceConfig(raw)
	if err != nil {
		return err
	}
	if bytes.Equal(next, raw) {
		fmt.Println("配置已使用设备协议版本 2")
		return nil
	}
	dir := *state
	if dir == "" {
		dir, err = app.StateDir(cfg.Node.Name)
		if err != nil {
			return err
		}
	}
	if err := privdir.Ensure(dir); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if endpoint, err := control.ReadEndpoint(dir); err == nil {
		response, err := control.NewClient(endpoint).Do(ctx, "GET", "/v2/status", nil, nil)
		if response != nil || err != nil && errors.Is(err, control.ErrVersionMismatch) {
			return fmt.Errorf("请先停止节点 %s，再升级配置；旧会话将在升级时结束", cfg.Node.Name)
		}
	}
	lockCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
	lock, err := filelock.Acquire(lockCtx, filepath.Join(dir, "device-runtime.lock"))
	stop()
	if err != nil {
		return fmt.Errorf("节点正在运行，请先停止再升级配置")
	}
	defer lock.Close()
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, raw) {
		return fmt.Errorf("配置已被其他操作修改，请重新执行升级")
	}
	backup := path + ".pre-device-v2-" + time.Now().Format("20060102-150405.000000000")
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, relative := range []string{"lan/node.pem", "lan/node.key", "lan/peers.json", "lan/pair-password.json", "network/tsnet/tailscaled.state"} {
		source := filepath.Join(dir, filepath.FromSlash(relative))
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("旧设备状态不是普通文件: %s", relative)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		destination := filepath.Join(backup+".state", filepath.FromSlash(relative))
		if err := privdir.Ensure(filepath.Dir(destination)); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".xmesh-migrate-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(next); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	current, err = os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, raw) {
		return fmt.Errorf("配置已被其他操作修改，备份已保存，请重新执行升级")
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	fmt.Println("配置已升级为设备协议版本 2；请启动节点并重新配对。备份:", backup)
	return nil
}
