package exec

import (
	"context"
	"os"

	"github.com/tzf1003/xmesh/internal/delegateguard"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// ProcessHooks configures optional delegation tracking and Linux subreaper
// containment for a launched process.
type ProcessHooks struct {
	Subreaper bool
	Guard     *delegateguard.Guard
}

// RunStream runs argv for a caller that reads the output while the command runs, such as a delegate task.
// The policy engine supplies the isolation: the managed runner when one is installed, or the launcher form
// that exec.run uses. env is the complete environment, which the caller builds with Engine.Environment, and
// stdin is written to the command and then closed.
//
// out and errout receive the standard streams. The caller keeps ownership of them, and of its own copies of
// their write ends: the caller must close those after RunStream returns, so that a reader sees end of file
// once the command and every process holding the pipe have gone. started, when not nil, runs once the command
// has started. A start failure is only visible in the returned error, and started has not run by then.
// A ctx that is already done when the call begins means the command never runs, and started does not run.
//
// The result reports the exit code and whether ctx ended the command. A done ctx terminates the process group
// or job, as exec.run does. When the command exits on its own, the launcher form also terminates the rest of its
// process group, so a child it left behind neither keeps the output open nor runs on after the call. A managed run
// reclaims its sandbox when it closes. An error means the command never started, or that the isolation refused it.
func RunStream(ctx context.Context, p *policy.Engine, dir string, env []string, argv []string, stdin string, out, errout *os.File, started func()) (int, bool, error) {
	return RunStreamWithHooks(ctx, p, dir, env, argv, stdin, out, errout, started, ProcessHooks{})
}

// RunStreamWithHooks is RunStream with optional ProcessHooks for delegated task
// containment and process-tree registration.
func RunStreamWithHooks(ctx context.Context, p *policy.Engine, dir string, env []string, argv []string, stdin string, out, errout *os.File, started func(), hooks ProcessHooks) (int, bool, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, false, toolerr.Args("cmd")
	}
	if ctx.Err() != nil {
		// Checked before the start, so a command whose context is already done never runs. A cancellation that lands
		// after this check still ends the command as soon as it starts.
		return 1, true, nil
	}
	a := Args{Cmd: argv[0], Args: argv[1:], Stdin: stdin}
	if runner := p.SandboxRunner(); runner != nil {
		return runManagedStartedHooks(ctx, runner, dir, a, env, out, errout, started, hooks)
	}
	return runLaunchedStartedHooks(ctx, p, dir, a, env, out, errout, started, true, hooks)
}

// LookupCommand resolves cmd as the managed runner starts it: a bare name is searched on PATH, and an
// explicit path is relative to dir. It returns an invalid_args error when no executable is found.
func LookupCommand(cmd, dir string) (string, error) {
	return managedCommand(cmd, dir)
}
