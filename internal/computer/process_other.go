//go:build !windows

package computer

import "os/exec"

func prepareNativeProcess(cmd *exec.Cmd) {}
