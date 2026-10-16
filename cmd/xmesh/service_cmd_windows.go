package main

import (
	"fmt"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

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
