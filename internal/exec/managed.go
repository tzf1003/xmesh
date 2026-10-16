package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// managedCleanupLimit bounds the cleanup that follows a finished or cancelled
// execution. The cleanup context is detached from the call deadline: a timed-out
// command must still be terminated and its sandbox reclaimed.
const managedCleanupLimit = 90 * time.Second

// runManaged executes through a managed sandbox runner. The runner owns process
// creation; this function owns the standard streams and the call deadline. The
// execution is always closed before returning, so no process or workspace
// obligation outlives a successful tool result. An unfinished cleanup is
// reported instead of being hidden behind the command's own result.
func runManaged(ctx context.Context, runner sandbox.Runner, cwd string, a Args, env []string, out, errout *os.File) (int, bool, error) {
	command, e := managedCommand(a.Cmd, cwd)
	if e != nil {
		return 0, false, e
	}
	input, feed, e := os.Pipe()
	if e != nil {
		return 0, false, toolerr.Internal("exec.stdin", e)
	}
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		defer feed.Close()
		_, _ = feed.WriteString(a.Stdin)
	}()
	spec := sandbox.Spec{Args: append([]string{command}, a.Args...), Dir: cwd, Env: env}
	process, startErr := runner.Start(ctx, cwd, spec, sandbox.Streams{Stdin: input, Stdout: out, Stderr: errout})
	// The runner transfers private duplicates; this end is no longer needed.
	_ = input.Close()
	if startErr != nil {
		<-fed
		if ctx.Err() != nil {
			return 1, true, nil
		}
		return 0, false, toolerr.Internal("exec.start", startErr)
	}
	type result struct {
		code int
		err  error
	}
	waited := make(chan result, 1)
	go func() {
		code, err := process.Wait()
		waited <- result{code, err}
	}()
	timedOut := false
	var r result
	select {
	case r = <-waited:
	case <-ctx.Done():
		timedOut = true
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedCleanupLimit)
	defer cancel()
	closeErr := process.Close(cleanup)
	if timedOut {
		r = <-waited
	}
	<-fed
	if closeErr != nil {
		return 0, false, toolerr.Internal("exec.sandbox_cleanup", closeErr)
	}
	if r.err != nil && !timedOut {
		return 0, false, toolerr.Internal("exec.wait", r.err)
	}
	return r.code, timedOut, nil
}

// managedCommand resolves the executable on the host, because the runner needs
// the image path before the sandboxed process exists. An explicit relative path
// is relative to the command directory; a bare name is searched on PATH.
func managedCommand(cmd, cwd string) (string, error) {
	path := cmd
	if filepath.Base(cmd) != cmd || strings.ContainsAny(cmd, `/\`) {
		if filepath.VolumeName(cmd) != "" && !filepath.IsAbs(cmd) {
			return "", toolerr.Args("cmd")
		}
		if !filepath.IsAbs(cmd) {
			path = filepath.Join(cwd, cmd)
		}
	}
	// A bare name that only resolves relative to the current directory fails
	// with ErrDot; only an explicit path may name a workspace executable.
	resolved, err := osexec.LookPath(path)
	if err != nil {
		return "", toolerr.Args("cmd")
	}
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cwd, resolved)
	}
	return resolved, nil
}
