package main

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"
)

func detachSetup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
}
func prepareSetupCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
func setupServiceInstalled(node string) (bool, error) {
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return false, e
	}
	dir, e := windows.GetSystemDirectory()
	if e != nil {
		return false, e
	}
	_, e = taskCommand(dir, "/Query", "/TN", userTaskName(serviceName(node), user.User.Sid.String()), "/XML")
	if e != nil {
		return false, nil
	}
	return true, nil
}
