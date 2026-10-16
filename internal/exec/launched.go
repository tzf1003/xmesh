package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// runLaunched starts the command through the platform launcher form of the
// policy sandbox and owns its process group until it exits or the context ends.
func runLaunched(ctx context.Context, p *policy.Engine, cwd string, a Args, env []string, out, errout *os.File) (int, bool, error) {
	launch, argv, env, e := p.Sandbox(a.Cmd, a.Args, cwd, env)
	if e != nil {
		return 0, false, e
	}
	cmd := osexec.Command(launch, argv...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = strings.NewReader(a.Stdin)
	cmd.Stdout = out
	cmd.Stderr = errout
	waitCommand, finish, e := startCommand(cmd)
	if e != nil {
		if _, ok := e.(*osexec.Error); ok || os.IsNotExist(e) {
			return 0, false, toolerr.Args("cmd")
		}
		return 0, false, toolerr.Internal("exec.start", e)
	}
	defer finish()
	done := make(chan error, 1)
	go func() { done <- waitCommand() }()
	timedOut := false
	select {
	case e = <-done:
	case <-ctx.Done():
		timedOut = true
		terminate(cmd)
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case e = <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			kill(cmd)
			e = <-done
		}
		kill(cmd)
	}
	return exitCode(cmd, e), timedOut, nil
}
