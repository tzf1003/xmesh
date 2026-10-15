package winbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/tzf1003/xmesh/internal/winjob"
	"golang.org/x/sys/windows"
)

type executionJob struct {
	handle windows.Handle
	census *winjob.ProcessCensus
}

// executionProcesses owns all launches admitted through one live profile owner.
// Each launch has a private creation-bound Job and a census attached before any
// admission. The operation gate covers creation, sealing, reaping and release.
// Reaping seals all future launches, requests termination of every Job, then
// confirms each complete census and exit signal. Failed reaping retains every
// handle and the profile reference for retry. This in-process owner does not
// establish abnormal-host recovery or filesystem writer/census coordination.
type executionProcesses struct {
	operation chan struct{}
	profile   *Profile
	jobs      []executionJob
	limit     int
	sealed    bool
	reaped    bool
	closed    bool
}

func newExecutionProcesses(p *Profile, maxJobs int) (*executionProcesses, error) {
	if p == nil || maxJobs < 1 || maxJobs > 256 {
		return nil, fmt.Errorf("AppContainer execution requires a profile and 1..256 jobs")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil || p.deleted || p.processes != nil || p.standaloneProcessAttributes {
		return nil, fmt.Errorf("AppContainer execution profile is unavailable or already owned")
	}
	owner := &executionProcesses{operation: make(chan struct{}, 1), profile: p, limit: maxJobs}
	p.processes = owner
	return owner, nil
}

func (e *executionProcesses) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case e.operation <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-e.operation
			return nil, err
		}
		return func() { <-e.operation }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// start returns caller-owned process/thread handles. It always creates the root
// suspended, pins its verified Job identity, then resumes it under the admission
// gate. Standard/ConPTY handle inheritance has a separate integration gate.
// A failed launch can retain a Job obligation until reap and Close.
func (e *executionProcesses) start(ctx context.Context, args []string, directory string, environment []string) (windows.ProcessInformation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var process windows.ProcessInformation
	release, err := e.acquire(ctx)
	if err != nil {
		return process, err
	}
	defer release()
	if e.sealed || e.closed || len(e.jobs) >= e.limit {
		return process, fmt.Errorf("AppContainer execution admission is closed or at its Job limit")
	}
	if len(args) == 0 || args[0] == "" {
		return process, fmt.Errorf("AppContainer execution requires a command")
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return process, fmt.Errorf("AppContainer execution argument contains NUL")
		}
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return process, err
	}
	dir, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return process, err
	}
	env, err := Environment(environment)
	if err != nil {
		return process, err
	}
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	job, err := winjob.New()
	if err != nil {
		return process, err
	}
	census, err := winjob.NewProcessCensus(job)
	if err != nil {
		return process, errors.Join(err, windows.CloseHandle(job))
	}
	e.jobs = append(e.jobs, executionJob{handle: job, census: census})
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return process, err
	}
	defer attrs.Delete()
	if err := winjob.Apply(attrs, job); err != nil {
		return process, err
	}
	if err := e.profile.applyProcess(attrs, e); err != nil {
		return process, err
	}
	if err := ctx.Err(); err != nil {
		return process, err
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	flags := uint32(windows.CREATE_SUSPENDED | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(nil, command, nil, nil, false, flags, &block[0], dir, &startup.StartupInfo, &process); err != nil {
		return windows.ProcessInformation{}, err
	}
	err = census.RetainProcess(ctx, process.Process)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		_, err = windows.ResumeThread(process.Thread)
	}
	if err != nil {
		// Creation has crossed the kernel boundary. Seal admission and retain
		// the Job/census even when cancellation or root capture fails.
		e.sealed = true
		err = errors.Join(err, windows.TerminateJobObject(job, 1), windows.CloseHandle(process.Thread), windows.CloseHandle(process.Process))
		return windows.ProcessInformation{}, err
	}
	return process, nil
}

// reap requests termination before census confirmation so live descendants
// cannot keep extending a launch tree. It visits every Job before waiting for
// any one of them. Missing notifications still prevent successful confirmation;
// cleanup retains its profile reference even after a termination side effect.
func (e *executionProcesses) reap(ctx context.Context, exitCode uint32) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if e.closed {
		return windows.ERROR_INVALID_HANDLE
	}
	e.sealed = true
	e.reaped = false
	var result error
	for _, job := range e.jobs {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		result = errors.Join(result, windows.TerminateJobObject(job.handle, exitCode))
	}
	for _, job := range e.jobs {
		result = errors.Join(result, job.census.WaitEmpty(ctx))
	}
	result = errors.Join(result, ctx.Err())
	if result == nil {
		e.reaped = true
	}
	return result
}

// Close releases the live process owner only after successful reap. Callers
// retain separate ACL/journal/writer guards through resource reclamation.
func (e *executionProcesses) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	release, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if e.closed {
		return nil
	}
	if !e.reaped {
		return fmt.Errorf("AppContainer execution owner requires complete process reaping")
	}
	var result error
	for i := range e.jobs {
		job := &e.jobs[i]
		result = errors.Join(result, job.census.Close())
		if job.handle != 0 {
			if err := windows.CloseHandle(job.handle); err != nil {
				result = errors.Join(result, err)
			} else {
				job.handle = 0
			}
		}
	}
	if result != nil {
		return result
	}
	e.profile.mu.Lock()
	defer e.profile.mu.Unlock()
	if e.profile.processes != e {
		return fmt.Errorf("AppContainer execution profile owner changed")
	}
	e.profile.processes = nil
	e.closed = true
	return nil
}
