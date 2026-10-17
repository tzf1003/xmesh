package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
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

// removeUserTasksUnder deletes the logon tasks this user registered for xmesh nodes whose program
// lies under dir, and reports how many it removed. Tasks of other users, and of xmesh copies in
// other folders (a test node, say), are left alone.
func removeUserTasksUnder(dir string) (int, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, err
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, err
	}
	listed, err := taskCommand(directory, "/Query", "/FO", "CSV", "/NH")
	if err != nil {
		// With no scheduled tasks at all schtasks exits 1; there is nothing to remove then.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return 0, nil
		}
		return 0, err
	}
	reader := csv.NewReader(bytes.NewReader(listed))
	reader.FieldsPerRecord, reader.LazyQuotes = -1, true
	rows, err := reader.ReadAll()
	if err != nil {
		return 0, err
	}
	sid := user.User.Sid.String()
	removed := 0
	for _, row := range rows {
		if len(row) == 0 || !isUserTaskOf(row[0], sid) {
			continue
		}
		// A task that disappears between the listing and this query is simply not ours to remove any more.
		document, err := taskCommand(directory, "/Query", "/TN", row[0], "/XML")
		if err != nil {
			continue
		}
		program, err := taskProgram(taskDocumentText(document))
		if err != nil || !programUnder(program, dir) {
			continue
		}
		_, _ = taskCommand(directory, "/End", "/TN", row[0])
		if _, err := taskCommand(directory, "/Delete", "/TN", row[0], "/F"); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
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
