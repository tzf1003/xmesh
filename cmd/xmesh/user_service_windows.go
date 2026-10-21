package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

func installUserService(c *service.Config) error {
	// Task Scheduler's Hidden setting hides the task, not its console. Use
	// the GUI-subsystem build so neither the kernel nor Computer Host creates one.
	background := filepath.Join(filepath.Dir(c.Executable), "xmesh-background.exe")
	if err := requireBackgroundExecutable(background); err != nil {
		return err
	}
	copy := *c
	copy.Executable = background
	c = &copy
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

// removeUserTasksUnder deletes the logon tasks this user registered for xmesh nodes whose program
// lies under dir, and reports how many it removed. Tasks of other users, and of xmesh copies in
// other folders (a test node, say), are left alone.
func removeUserTasksUnder(dir string) (int, error) {
	return installerUserTasksUnder(dir, "")
}

func relocateUserTasksUnder(dir, destination string) (int, error) {
	return installerUserTasksUnder(dir, destination)
}

func installerUserTasksUnder(dir, destination string) (int, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, err
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, err
	}
	return updateUserTasksUnder(dir, destination, user.User.Sid.String(), func(args ...string) ([]byte, error) {
		return taskCommand(directory, args...)
	})
}

func taskCommand(systemDirectory string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(systemDirectory, "schtasks.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("Windows 用户任务操作失败: %w: %s", err, output)
	}
	return output, nil
}

func requireBackgroundExecutable(path string) error {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("缺少无控制台后台内核 %s；请使用完整发行包，或用 go build -ldflags=-H=windowsgui -o <内核目录>/xmesh-background.exe ./cmd/xmesh 构建", path)
	}
	return validateBackgroundExecutable(path)
}
