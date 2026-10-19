//go:build !windows && !linux && !darwin

package exec

import (
	"fmt"
	"os/exec"
)

func startTreeCommand(cmd *exec.Cmd) (func() error, func(), error) { return startCommand(cmd) }

func watchTree(int) (func() error, error) {
	return nil, fmt.Errorf("managed backend process trees are unsupported on this platform")
}
