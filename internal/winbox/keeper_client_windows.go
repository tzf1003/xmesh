package winbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// guardKeeper is the host-side handle of an independent keeper process. It is an
// io.Closer suitable as the writer guard of workspaceExecutionPrepare: Close
// asks the keeper to release, and the keeper agrees only when it has itself
// verified a durable completion for the bound journal. If the host dies first,
// the keeper keeps every namespace hold for a later recovery owner.
type guardKeeper struct {
	operation chan struct{}
	mu        sync.Mutex
	input     *os.File
	output    *os.File
	process   windows.Handle
	done      chan struct{}
	waitErr   error // Published by closing done.
	sequence  uint64
	failed    error
	released  bool
	key       journalObject
}

type keeperLaunch struct {
	Workspace, Journal string
	// Command is the executable and leading arguments; nil selects this binary's
	// internal keeper entry point.
	Command []string
}

// launchGuardKeeper pins the workspace and journal-directory chains in a new
// process before any journal, profile or grant exists. The caller owns the
// returned keeper and must Close it (retrying on error) after the journal is
// complete and closed.
func launchGuardKeeper(ctx context.Context, spec keeperLaunch) (_ *guardKeeper, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := spec.Command
	if command == nil {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		command = []string{executable, "__sandbox-guard"}
	}
	if len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("guard keeper executable unavailable")
	}
	workspace, err := openWorkspaceRoot(ctx, spec.Workspace)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	state, err := openWorkspaceRoot(ctx, filepath.Dir(spec.Journal))
	if err != nil {
		return nil, err
	}
	defer state.Close()

	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer requestRead.Close()
	replyRead, replyWrite, err := os.Pipe()
	if err != nil {
		requestWrite.Close()
		return nil, err
	}
	defer replyWrite.Close()
	s := &guardKeeper{operation: make(chan struct{}, 1), input: replyRead, output: requestWrite, done: make(chan struct{})}
	defer func() {
		if resultErr != nil && s.process == 0 {
			s.closeLocal()
		}
	}()
	var inherited []windows.Handle
	defer func() {
		for _, h := range inherited {
			windows.CloseHandle(h)
		}
	}()
	duplicate := func(source windows.Handle, access uint32) (windows.Handle, error) {
		var h windows.Handle
		options := uint32(0)
		if access == 0 {
			options = windows.DUPLICATE_SAME_ACCESS
		}
		err := windows.DuplicateHandle(windows.CurrentProcess(), source, windows.CurrentProcess(), &h, access, true, options)
		if err == nil {
			inherited = append(inherited, h)
		}
		return h, err
	}
	for _, source := range []windows.Handle{windows.Handle(requestRead.Fd()), windows.Handle(replyWrite.Fd()), windows.CurrentProcess()} {
		access := uint32(0)
		if source == windows.CurrentProcess() {
			access = windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION
		}
		if _, err := duplicate(source, access); err != nil {
			return nil, err
		}
	}
	init := &keeperInit{Journal: spec.Journal}
	listed := make(map[journalObject]bool)
	add := func(source windows.Handle, role string) error {
		object, err := inspectTreeObject(source, "")
		if err != nil {
			return err
		}
		identity := journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory}
		if !object.directory || listed[identity] {
			if !object.directory {
				return fmt.Errorf("guard keeper requires directory namespace holds")
			}
			return nil
		}
		h, err := duplicate(source, 0)
		if err != nil {
			return err
		}
		listed[identity] = true
		init.Holds = append(init.Holds, keeperHold{Handle: uint64(h), Object: identity, Role: role})
		if role == keeperRoleRoot {
			init.Key = identity
		}
		return nil
	}
	for _, chain := range []struct {
		tree *workspaceTree
		last string
	}{{workspace, keeperRoleRoot}, {state, keeperRoleJourn}} {
		for i, h := range chain.tree.ancestors {
			role := keeperRoleParent
			if i == 0 {
				role = keeperRoleAnchor
			}
			if err := add(h, role); err != nil {
				return nil, err
			}
		}
		if err := add(chain.tree.objects[0].handle, chain.last); err != nil {
			return nil, err
		}
	}
	s.key = init.Key
	if err := (keeperRequest{Sequence: 1, Operation: "init", Init: init}).validate(); err != nil {
		return nil, err
	}
	args := append([]string(nil), command[1:]...)
	for _, h := range inherited[:3] {
		args = append(args, strconv.FormatUint(uint64(h), 10))
	}
	cmd := exec.Command(command[0], args...)
	cmd.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot")}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	for _, h := range inherited {
		cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(h))
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Only the child keeps the inherited ends. A reply writer left in the host
	// would hide an early keeper exit; a request reader would hide host death.
	for _, h := range inherited {
		windows.CloseHandle(h)
	}
	inherited = nil
	err = cmd.Process.WithHandle(func(handle uintptr) {
		resultErr = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(), &s.process,
			windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_DUP_HANDLE|windows.PROCESS_TERMINATE, false, 0)
	})
	if err != nil || resultErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.Join(err, resultErr)
	}
	go func() { s.waitErr = cmd.Wait(); close(s.done) }()
	requestRead.Close()
	replyWrite.Close()
	if _, err := s.call(ctx, keeperRequest{Operation: "init", Init: init}, nil); err == nil {
		return s, nil
	} else {
		// No obligation exists yet. Terminate the unstarted keeper and confirm
		// its exit so a rollback cannot leave a stray holder of the namespaces.
		killErr := windows.TerminateProcess(s.process, 1)
		result, waitErr := windows.WaitForSingleObject(s.process, 30000)
		if waitErr != nil || result != windows.WAIT_OBJECT_0 {
			return s, errors.Join(err, killErr, waitErr, fmt.Errorf("unstarted guard keeper exit unconfirmed: %d", result))
		}
		<-s.done
		s.operation <- struct{}{}
		s.closeLocal()
		<-s.operation
		return nil, err
	}
}

// call keeps exchanges ordered even after a caller cancels: the background
// exchange finishes and later calls wait for it.
func (s *guardKeeper) call(ctx context.Context, request keeperRequest, prepare func(*keeperRequest) error) (keeperReply, error) {
	var empty keeperReply
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	select {
	case s.operation <- struct{}{}:
	case <-ctx.Done():
		return empty, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-s.operation
		return empty, err
	}
	s.mu.Lock()
	if s.failed != nil || s.released {
		err := errors.Join(s.failed, fmt.Errorf("guard keeper channel unavailable"))
		s.mu.Unlock()
		<-s.operation
		return empty, err
	}
	request.Sequence = s.sequence + 1
	s.mu.Unlock()
	if prepare != nil {
		if err := prepare(&request); err != nil {
			<-s.operation
			return empty, err
		}
	}
	if err := request.validate(); err != nil {
		<-s.operation
		return empty, err
	}
	s.mu.Lock()
	s.sequence = request.Sequence
	s.mu.Unlock()
	finished := make(chan struct{})
	var reply keeperReply
	var exchangeErr error
	go func() {
		defer func() { <-s.operation; close(finished) }()
		err := writeSupervisorFrame(s.output, request)
		if err == nil {
			err = readSupervisorFrame(s.input, &reply)
		}
		if err == nil && reply.Sequence != request.Sequence {
			err = fmt.Errorf("guard keeper response sequence mismatch")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			s.failed = errors.Join(s.failed, err)
			exchangeErr = err
		} else if reply.Error != "" {
			exchangeErr = fmt.Errorf("guard keeper: %s", reply.Error)
		}
	}()
	select {
	case <-finished:
		return reply, exchangeErr
	case <-ctx.Done():
		return empty, ctx.Err()
	}
}

// bindJournal reports the created journal's identity so the keeper can later
// verify that exact file's completion without trusting the host.
func (s *guardKeeper) bindJournal(ctx context.Context, j *grantJournal) error {
	if j == nil || j.file == nil {
		return fmt.Errorf("guard keeper requires an open journal")
	}
	object, err := inspectTreeObject(windows.Handle(j.file.Fd()), "")
	if err != nil || object.directory {
		return fmt.Errorf("guard keeper journal identity unavailable: %v", err)
	}
	identity := journalObject{Volume: object.id.Volume, ID: object.id.ID}
	_, err = s.call(ctx, keeperRequest{Operation: "journal", Journal: &identity}, nil)
	return err
}

// Hold gives the keeper an additional directory identity (for example an import
// source guard) that must stay pinned through recovery. The handle stays owned
// by the caller; the keeper receives its own duplicate.
func (s *guardKeeper) Hold(ctx context.Context, handle windows.Handle) error {
	object, err := inspectTreeObject(handle, "")
	if err != nil {
		return err
	}
	identity := journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory}
	_, err = s.call(ctx, keeperRequest{Operation: "hold"}, func(r *keeperRequest) error {
		var remote windows.Handle
		err := windows.DuplicateHandle(windows.CurrentProcess(), handle, s.process, &remote, 0, false, windows.DUPLICATE_SAME_ACCESS)
		r.Hold = &keeperHold{Handle: uint64(remote), Object: identity, Role: keeperRoleImport}
		return err
	})
	return err
}

func (s *guardKeeper) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.closeContext(ctx)
}

// closeContext is retry safe while the keeper refuses release. A keeper that
// exited without an accepted release no longer protects anything; that loss is
// reported on every call rather than silently treated as completion.
func (s *guardKeeper) closeContext(ctx context.Context) error {
	s.mu.Lock()
	released := s.released
	s.mu.Unlock()
	if released {
		return nil
	}
	if _, err := s.call(ctx, keeperRequest{Operation: "release"}, nil); err != nil {
		select {
		case <-s.done:
			// The reply may have been lost after an accepted release.
		default:
			return err
		}
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.waitErr != nil {
		return errors.Join(errKeeperLost, s.waitErr)
	}
	select {
	case s.operation <- struct{}{}:
		defer func() { <-s.operation }()
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.closeLocal()
	if err == nil {
		s.released = true
	}
	return err
}

func (s *guardKeeper) closeLocal() error {
	var result error
	if s.process != 0 {
		if err := windows.CloseHandle(s.process); err != nil {
			result = errors.Join(result, err)
		} else {
			s.process = 0
		}
	}
	for _, file := range []**os.File{&s.input, &s.output} {
		if *file != nil {
			result = errors.Join(result, (*file).Close())
			*file = nil
		}
	}
	return result
}
