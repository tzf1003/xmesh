package winbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/winjob"
	"golang.org/x/sys/windows"
)

type processSupervisor struct {
	operation         chan struct{}
	mu                sync.Mutex
	input             *os.File
	output            *os.File
	process           windows.Handle
	done              chan struct{}
	waitErr           error // Published by closing done.
	sequence          uint64
	failed            error
	jobs              map[uint32]windows.Handle
	closed            bool
	closeAcknowledged bool
}

// A returned owner accompanying an error retains its execution obligation.
// A nil command selects the same installed binary's internal helper entry point.
func newSupervisedExecutionProcesses(ctx context.Context, p *Profile, maxJobs int, command []string) (*executionProcesses, error) {
	owner, err := newExecutionProcesses(p, maxJobs)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	owner.supervisor, err = launchProcessSupervisor(ctx, p.journal, owner.intent, maxJobs, command)
	if err != nil {
		owner.sealed = true
	}
	return owner, err
}

func launchProcessSupervisor(ctx context.Context, j *grantJournal, intent uint64, limit int, command []string) (_ *processSupervisor, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if j == nil || j.file == nil || j.blocked != nil || intent == 0 || intent > uint64(len(j.records)) || !j.state.executions[intent] {
		return nil, fmt.Errorf("process supervision requires a pending owned execution journal")
	}
	directory, ok := j.namespace.(*journalDirectory)
	if !ok || directory.tree == nil || j.boundary == nil {
		return nil, fmt.Errorf("process supervision requires bound journal namespace handles")
	}
	if command == nil {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		command = []string{executable, "__sandbox-supervisor"}
	}
	if len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("process supervisor executable unavailable")
	}
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
	s := &processSupervisor{operation: make(chan struct{}, 1), input: replyRead, output: requestWrite, jobs: make(map[uint32]windows.Handle), done: make(chan struct{})}
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
			access = windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_DUP_HANDLE
		}
		if _, err := duplicate(source, access); err != nil {
			return nil, err
		}
	}
	init := &supervisorInit{Limit: limit, Intent: intent, ExecutionID: j.records[intent-1].ExecutionID}
	bind := func(source windows.Handle) (supervisorGuard, error) {
		object, err := inspectTreeObject(source, "")
		if err != nil {
			return supervisorGuard{}, err
		}
		h, err := duplicate(source, 0)
		return supervisorGuard{Handle: uint64(h), Object: journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory}}, err
	}
	init.Journal, err = bind(windows.Handle(j.file.Fd()))
	if err != nil {
		return nil, err
	}
	for _, source := range append([]windows.Handle{directory.tree.objects[0].handle}, directory.tree.ancestors...) {
		guard, err := bind(source)
		if err != nil {
			return nil, err
		}
		init.Guards = append(init.Guards, guard)
	}
	if err := (supervisorRequest{Sequence: 1, Operation: "init", Init: init}).validate(); err != nil {
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
	// Only the child keeps these inherited ends after startup. Keeping a reply
	// writer in the host would suppress EOF when bootstrap exits prematurely.
	for _, h := range inherited {
		windows.CloseHandle(h)
	}
	inherited = nil
	// WithHandle pins the actual created process, including immediate exit.
	// PID-based reopening could instead adopt a reused PID after startup failure.
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
	// Closing the original child pipe ends is essential: host death must leave
	// no writer outside the host and the explicitly inherited supervisor end.
	requestRead.Close()
	replyWrite.Close()
	_, err = s.call(ctx, supervisorRequest{Operation: "init", Init: init})
	if err == nil {
		return s, nil
	}
	// Initialization has admitted no Jobs or sandbox roots. Terminate this
	// unstarted helper and confirm its exit before an empty-owner rollback.
	killErr := windows.TerminateProcess(s.process, 1)
	result, waitErr := windows.WaitForSingleObject(s.process, 30000)
	if waitErr != nil || result != windows.WAIT_OBJECT_0 {
		return s, errors.Join(err, killErr, waitErr, fmt.Errorf("unstarted supervisor exit unconfirmed: %d", result))
	}
	<-s.done
	// The pending init reader finishes at helper exit. Preserve its serialization
	// before releasing borrowed handles and allowing owner rollback.
	s.operation <- struct{}{}
	s.closeLocal()
	<-s.operation
	return nil, err
}

// Calls remain ordered even when a caller cancels after sending a request. The
// bounded helper operation and its response finish in one background exchange;
// later calls wait for that exchange. Every returned Job handle is retained in
// the client even if the initiating caller stopped waiting for its response.
func (s *processSupervisor) call(ctx context.Context, request supervisorRequest) (supervisorReply, error) {
	return s.callPrepared(ctx, request, nil)
}

func (s *processSupervisor) callPrepared(ctx context.Context, request supervisorRequest, prepare func(*supervisorRequest) error) (supervisorReply, error) {
	var empty supervisorReply
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
	if request.Operation == "close" && s.closeAcknowledged {
		s.mu.Unlock()
		<-s.operation
		return empty, nil
	}
	if s.failed != nil || s.closed {
		err := errors.Join(s.failed, fmt.Errorf("process supervisor channel unavailable"))
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
	done := make(chan struct{})
	var reply supervisorReply
	var exchangeErr error
	go func() {
		defer func() { <-s.operation; close(done) }()
		err := writeSupervisorFrame(s.output, request)
		if err == nil {
			err = readSupervisorFrame(s.input, &reply)
		}
		if err == nil && reply.Sequence != request.Sequence {
			err = fmt.Errorf("process supervisor response sequence mismatch")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err == nil && reply.Error == "" && request.Operation == "new-job" {
			h := windows.Handle(reply.Handle)
			if reply.Job != uint32(len(s.jobs)+1) || h == 0 || h == windows.InvalidHandle || uint64(h) != reply.Handle {
				err = fmt.Errorf("invalid process supervisor Job response")
			} else {
				// Keep every valid borrowed handle through owner release, including
				// verification errors and responses arriving after cancellation.
				s.jobs[reply.Job] = h
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				err = winjob.WaitEmpty(ctx, h)
				cancel()
			}
		} else if err == nil && (reply.Job != 0 || reply.Handle != 0) {
			err = fmt.Errorf("unexpected process supervisor response handles")
		}
		if err == nil && reply.Error == "" && request.Operation == "close" {
			s.closeAcknowledged = true
		}
		if err != nil {
			s.failed = errors.Join(s.failed, err)
			exchangeErr = err
		} else if reply.Error != "" {
			exchangeErr = fmt.Errorf("process supervisor: %s", reply.Error)
		}
	}()
	select {
	case <-done:
		return reply, exchangeErr
	case <-ctx.Done():
		return empty, ctx.Err()
	}
}

func (s *processSupervisor) retain(ctx context.Context, job uint32, process windows.Handle) error {
	_, err := s.callPrepared(ctx, supervisorRequest{Operation: "retain", Job: job}, func(request *supervisorRequest) error {
		var remote windows.Handle
		err := windows.DuplicateHandle(windows.CurrentProcess(), process, s.process, &remote,
			windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0)
		request.Handle = uint64(remote)
		return err
	})
	return err
}

func (s *processSupervisor) close(ctx context.Context) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil
	}
	if _, err := s.call(ctx, supervisorRequest{Operation: "close"}); err != nil {
		return err
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.waitErr != nil {
		return s.waitErr
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
		s.closed = true
	}
	return err
}

func (s *processSupervisor) closeLocal() error {
	var result error
	for id, h := range s.jobs {
		if err := windows.CloseHandle(h); err != nil {
			result = errors.Join(result, err)
		} else {
			delete(s.jobs, id)
		}
	}
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
