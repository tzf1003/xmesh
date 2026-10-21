//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func detachSetup(cmd *exec.Cmd)                       { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func prepareSetupCommand(cmd *exec.Cmd)               {}
func setupServiceInstalled(node string) (bool, error) { return serviceInstalledWithLibrary(node) }
