//go:build !windows

package main

import (
	"fmt"

	"github.com/kardianos/service"
)

func installUserService(*service.Config) error {
	return fmt.Errorf("Windows 用户任务仅适用于 Windows")
}
