package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

func installUserService(c *service.Config) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	name, err := createUserTask(c, user.User.Sid.String(), windows.ComposeCommandLine(c.Arguments), func(name, path string) error {
		_, err := taskCommand(directory, "/Create", "/TN", name, "/XML", path)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Println("已安装登录任务:", name)
	fmt.Println("下次登录时启动；保持该用户登录以运行 GUI 后端。")
	return nil
}

func taskCommand(systemDirectory string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(systemDirectory, "schtasks.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("Windows 用户任务操作失败: %w: %s", err, output)
	}
	return output, nil
}
