package main

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

// errTaskMissing means the logon task of this node is not registered for the current user.
var errTaskMissing = fmt.Errorf("%w: 登录任务不存在", service.ErrNotInstalled)

// platformServiceMissing recognises the SCM's "no such service".
func platformServiceMissing(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
}

// userTaskAction drives the logon task that install registers for the current user.
func userTaskAction(action string, c *service.Config) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	name := userTaskName(c.Name, user.User.Sid.String())
	// A task that was never registered is reported by schtasks in the system language, so ask
	// it directly: /Query exits 1 when there is no such task.
	if _, err := taskCommand(directory, "/Query", "/TN", name); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return errTaskMissing
		}
		return err
	}
	run := func(args ...string) error { _, err := taskCommand(directory, args...); return err }
	switch action {
	case "uninstall":
		return run("/Delete", "/TN", name, "/F")
	case "start":
		return run("/Run", "/TN", name)
	case "stop":
		return run("/End", "/TN", name)
	case "restart":
		// /End fails when the task is not running; restart then just starts it.
		_ = run("/End", "/TN", name)
		return run("/Run", "/TN", name)
	}
	return fmt.Errorf("未知服务操作: %s", action)
}

// installerSID is the SID of the user running install, or "" if unavailable.
func installerSID() string {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}
