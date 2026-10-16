// Package sandbox defines the platform-neutral boundary between the tools and a
// managed isolation runner that owns process creation, lifetime and cleanup.
//
// Launcher-style isolation (bubblewrap, sandbox-exec) rewrites argv and lets the
// tool start the process itself. A managed runner cannot be expressed that way:
// it creates the process, holds the identities needed to reclaim the sandbox
// afterwards, and must be the party that terminates and releases the execution.
// The tools therefore hand the runner a workspace and a command and receive an
// owner for the whole execution.
package sandbox

import (
	"context"
	"io"
	"os"
)

// Spec is a fully resolved command. Args[0] is an absolute executable path.
type Spec struct {
	Args []string
	Dir  string
	Env  []string
}

// Streams are caller-owned capabilities. The runner never closes them. A nil
// Stdin means an empty input. Stdout and Stderr must be set and may be the same
// file.
type Streams struct {
	Stdin, Stdout, Stderr *os.File
}

// Process owns one sandboxed execution.
type Process interface {
	// PID identifies the root process.
	PID() int
	// Wait blocks until the root process exits, or Close terminates it, and
	// returns its exit code. It does not release the execution.
	Wait() (int, error)
	// Close terminates every process of the execution, confirms that none
	// remain, releases the sandbox and reclaims the workspace. It is safe to
	// call more than once and concurrently with Wait. A non-nil error means the
	// execution still holds obligations; Close may be retried.
	Close(ctx context.Context) error
}

// Terminal is the PTY form of an execution. Close terminates and releases it.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Signal(string) error
	Wait() error
	PID() int
}

// Runner creates sandboxed executions bound to a workspace directory.
type Runner interface {
	Start(ctx context.Context, workspace string, spec Spec, streams Streams) (Process, error)
	StartTerminal(ctx context.Context, workspace string, spec Spec, cols, rows int) (Terminal, error)
}
