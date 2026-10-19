package exec

import (
	"os/exec"
	"sync"
)

// ProcessTree is an opt-in lifetime boundary for stdio backends. It reuses the
// native suspended/Job launch on Windows and tracks detached descendants on Unix.
// Call Wait once and Close after the root exits or must be forcibly terminated.
type ProcessTree struct {
	wait    func() error
	finish  func()
	cleanup func() error
	once    sync.Once
	err     error
}

func StartProcessTree(cmd *exec.Cmd) (*ProcessTree, error) {
	wait, finish, err := startCommand(cmd)
	if err != nil {
		return nil, err
	}
	cleanup, err := watchTree(cmd.Process.Pid)
	if err != nil {
		kill(cmd)
		finish()
		_ = wait()
		return nil, err
	}
	return &ProcessTree{wait: wait, finish: finish, cleanup: cleanup}, nil
}

func (p *ProcessTree) Wait() error { return p.wait() }
func (p *ProcessTree) Close() error {
	p.once.Do(func() { p.finish(); p.err = p.cleanup() })
	return p.err
}
