package pty

import (
	"context"
	"time"

	"github.com/tzf1003/xmesh/internal/sandbox"
)

// managedStartLimit bounds sandbox preparation for one terminal. Workspace
// admission scans the directory tree and starts a process supervisor, so it is
// allowed longer than a direct launch.
const managedStartLimit = 60 * time.Second

// startManaged asks a managed sandbox runner for a terminal bound to the
// session's workspace. The returned terminal owns the execution; closing it
// terminates the process tree and releases the sandbox.
func startManaged(runner sandbox.Runner, shell, cwd string, env []string, cols, rows int) (terminal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), managedStartLimit)
	defer cancel()
	return runner.StartTerminal(ctx, cwd, sandbox.Spec{Args: []string{shell}, Dir: cwd, Env: env}, cols, rows)
}
