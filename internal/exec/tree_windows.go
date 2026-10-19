//go:build windows

package exec

import "os/exec"

func startTreeCommand(cmd *exec.Cmd) (func() error, func(), error) {
	return startCommandWithWindow(cmd, true)
}

// startCommand binds the whole tree to a kill-on-close Job before it runs.
func watchTree(int) (func() error, error) { return func() error { return nil }, nil }
