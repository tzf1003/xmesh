package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/delegateguard"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// runLaunched starts the command through the platform launcher form of the
// policy sandbox and owns its process group until it exits or the context ends.
func runLaunched(ctx context.Context, p *policy.Engine, cwd string, a Args, env []string, out, errout *os.File) (int, bool, error) {
	return runLaunchedStartedHooks(ctx, p, cwd, a, env, out, errout, nil, false, ProcessHooks{})
}

func runLaunchedHooks(ctx context.Context, p *policy.Engine, cwd string, a Args, env []string, out, errout *os.File, hooks ProcessHooks) (int, bool, error) {
	return runLaunchedStartedHooks(ctx, p, cwd, a, env, out, errout, nil, false, hooks)
}

// runLaunchedStarted is runLaunched that also calls started, when it is not nil, once the process exists. When sweep
// is true, the process group is also terminated after the command has exited on its own, so that a child the command
// left behind cannot keep running, or keep the output open, once the caller has finished with the command.
func runLaunchedStarted(ctx context.Context, p *policy.Engine, cwd string, a Args, env []string, out, errout *os.File, started func(), sweep bool) (int, bool, error) {
	return runLaunchedStartedHooks(ctx, p, cwd, a, env, out, errout, started, sweep, ProcessHooks{})
}

func runLaunchedStartedHooks(ctx context.Context, p *policy.Engine, cwd string, a Args, env []string, out, errout *os.File, started func(), sweep bool, hooks ProcessHooks) (int, bool, error) {
	launch, argv, env, e := p.Sandbox(a.Cmd, a.Args, cwd, env)
	if e != nil {
		return 0, false, e
	}
	var reaper *delegateguard.ReaperPipes
	if hooks.Subreaper && launch == a.Cmd {
		if wl, wa, rp, ok := delegateguard.WrapCommand(launch, argv); ok {
			launch, argv, reaper = wl, wa, rp
		}
	}
	cmd := osexec.Command(launch, argv...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = strings.NewReader(a.Stdin)
	if reaper != nil {
		reaper.Apply(cmd)
	}
	if sweep {
		// Wait also waits for the copy into stdin. A child the command left behind can hold that pipe without
		// reading it, which would keep Wait from returning, so the sweep below would never run. WaitDelay closes
		// the pipe a short time after the command exits.
		cmd.WaitDelay = time.Second
	}
	cmd.Stdout = out
	cmd.Stderr = errout
	waitCommand, finish, e := startCommandWithGuard(cmd, hooks.Guard)
	if reaper != nil {
		reaper.CloseChildEnds()
	}
	if e != nil {
		if reaper != nil {
			reaper.CloseAll()
		}
		if _, ok := e.(*osexec.Error); ok || os.IsNotExist(e) {
			return 0, false, toolerr.Args("cmd")
		}
		return 0, false, toolerr.Internal("exec.start", e)
	}
	if reaper != nil {
		if startErr := reaper.WaitStart(); startErr != nil {
			reaper.CloseDeath()
			_ = waitCommand()
			finish()
			return 0, false, toolerr.Internal("exec.start", startErr)
		}
	}
	defer func() {
		if reaper != nil {
			reaper.CloseDeath()
		}
		finish()
	}()
	if started != nil {
		started()
	}
	done := make(chan error, 1)
	go func() { done <- waitCommand() }()
	timedOut := false
	stopReaper := func() {
		if reaper != nil {
			reaper.CloseDeath()
			grace := time.NewTimer(300 * time.Millisecond)
			select {
			case e = <-done:
				if !grace.Stop() {
					<-grace.C
				}
				return
			case <-grace.C:
			}
		}
		kill(cmd)
		e = <-done
	}
	select {
	case e = <-done:
		if sweep {
			kill(cmd)
		}
	case <-ctx.Done():
		timedOut = true
		terminate(cmd)
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case e = <-done:
			if !timer.Stop() {
				<-timer.C
			}
			kill(cmd)
		case <-timer.C:
			stopReaper()
		}
	}
	return exitCode(cmd, e), timedOut, nil
}
