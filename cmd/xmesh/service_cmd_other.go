//go:build !windows

package main

import (
	"fmt"

	"github.com/kardianos/service"
)

func userTaskAction(string, *service.Config) error {
	return fmt.Errorf("Windows 用户任务仅适用于 Windows")
}

func removeUserTasksUnder(string) (int, error) {
	return 0, fmt.Errorf("Windows 用户任务仅适用于 Windows")
}

func installerSID() string { return "" }

func platformServiceMissing(error) bool { return false }
