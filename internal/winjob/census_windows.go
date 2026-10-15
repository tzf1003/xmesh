package winjob

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	isProcessInJob     = jobKernel32.NewProc("IsProcessInJob")
	getJobNotification = jobKernel32.NewProc("GetQueuedCompletionStatus")
)

// Job notifications use the overlapped slot as an integer PID, not a pointer.
// Keep it in uintptr storage so the Go GC never scans a synthetic low pointer.
func dequeueJobNotification(port windows.Handle) (code uint32, key, value uintptr, err error) {
	ok, _, callErr := getJobNotification.Call(uintptr(port), uintptr(unsafe.Pointer(&code)), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value)), uintptr(windows.INFINITE))
	if ok == 0 {
		err = callErr
	}
	return
}

// ProcessCensus retains identity handles from Job creation notifications,
// including OS-created console hosts. Notifications are not guaranteed: the
// kernel's cumulative assigned count must match the captured identities before
// any successful exit confirmation. A lost/raced identity retains an error.
// The caller owns admission coordination and the original job handle. Construct
// this observer before the first admission, on a private, empty job with no
// existing completion-port association. Close it after resource reclamation.
type ProcessCensus struct {
	operation chan struct{} // Keeps borrowed handles live through confirmation.
	mu        sync.Mutex
	job, port windows.Handle
	processes map[uint32]windows.Handle
	err       error
	closed    bool
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func NewProcessCensus(job windows.Handle) (*ProcessCensus, error) {
	if job == 0 || job == windows.InvalidHandle {
		return nil, windows.ERROR_INVALID_HANDLE
	}
	if err := isProcessInJob.Find(); err != nil {
		return nil, err
	}
	if _, err := queryProcessAccounting(job, 0); err != nil {
		return nil, err
	}
	var owned windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), job, windows.CurrentProcess(), &owned, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	port, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
	if err != nil {
		_ = windows.CloseHandle(owned)
		return nil, err
	}
	info := struct {
		Key  uintptr
		Port windows.Handle
	}{Key: 2, Port: port}
	if _, err := windows.SetInformationJobObject(owned, windows.JobObjectAssociateCompletionPortInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(port)
		_ = windows.CloseHandle(owned)
		return nil, err
	}
	c := &ProcessCensus{operation: make(chan struct{}, 1), job: owned, port: port, processes: make(map[uint32]windows.Handle), done: make(chan struct{})}
	go c.observe()
	return c, nil
}

func (c *ProcessCensus) observe() {
	defer close(c.done)
	for {
		code, key, value, err := dequeueJobNotification(c.port)
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		if err != nil {
			c.err = errors.Join(c.err, fmt.Errorf("job process notification: %w", err))
			c.mu.Unlock()
			return
		}
		if key != 2 {
			c.err = errors.Join(c.err, fmt.Errorf("unexpected job process notification key"))
			c.mu.Unlock()
			return
		}
		if code == 6 { // JOB_OBJECT_MSG_NEW_PROCESS; other events do not release identities.
			if value > uintptr(^uint32(0)) {
				c.err = errors.Join(c.err, ErrIncompleteProcessSet)
			} else {
				c.capture(uint32(value))
			}
		}
		failed := c.err != nil
		c.mu.Unlock()
		if failed {
			return
		}
	}
}

// Called with mu held. Membership is checked against the pinned Job handle;
// a recycled PID outside this Job never enters the retained census. Keeping the
// handle prevents later reuse; a missed earlier identity leaves a count error.
func (c *ProcessCensus) capture(pid uint32) {
	if _, exists := c.processes[pid]; exists {
		return
	}
	if len(c.processes) >= 10000 || pid == 0 {
		c.err = errors.Join(c.err, ErrIncompleteProcessSet)
		return
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err == nil {
		var member int32
		ok, _, callErr := isProcessInJob.Call(uintptr(process), uintptr(c.job), uintptr(unsafe.Pointer(&member)))
		if ok == 0 {
			err = callErr
		} else if member == 0 {
			err = fmt.Errorf("not a member of the observed job")
		}
	}
	if err != nil {
		if process != 0 {
			_ = windows.CloseHandle(process)
		}
		c.err = errors.Join(c.err, fmt.Errorf("%w: retain notified process %d: %v", ErrIncompleteProcessSet, pid, err))
		return
	}
	c.processes[pid] = process
}

// WaitEmpty applies the same admission/quiescence conditions as the package
// function, using the observer's owned identity handles. Only this Job is proven.
func (c *ProcessCensus) WaitEmpty(ctx context.Context) error { return c.confirm(ctx, false, 0) }

// TerminateAndWait first waits for a complete census, then terminates and waits
// for every retained member's exit signal. Failed capture/confirmation keeps the
// handles for retry; durable cleanup obligations remain the caller's concern.
func (c *ProcessCensus) TerminateAndWait(ctx context.Context, exitCode uint32) error {
	return c.confirm(ctx, true, exitCode)
}

func (c *ProcessCensus) confirm(ctx context.Context, terminate bool, exitCode uint32) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case c.operation <- struct{}{}:
		defer func() { <-c.operation }()
	case <-ctx.Done():
		return ctx.Err()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var pending error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, pending)
		}
		c.mu.Lock()
		if c.closed || c.err != nil {
			err := c.err
			if c.closed {
				err = errors.Join(err, windows.ERROR_INVALID_HANDLE)
			}
			c.mu.Unlock()
			return err
		}
		processes := make([]windows.Handle, 0, len(c.processes))
		for _, process := range c.processes {
			processes = append(processes, process)
		}
		c.mu.Unlock()
		_, err := queryProcessAccounting(c.job, len(processes))
		if err == nil {
			if terminate {
				err = TerminateAndWait(ctx, c.job, exitCode, processes...)
			} else {
				err = WaitEmpty(ctx, c.job, processes...)
			}
			c.mu.Lock()
			err = errors.Join(err, c.err, ctx.Err())
			c.mu.Unlock()
			return err
		}
		if !errors.Is(err, ErrIncompleteProcessSet) {
			return err
		}
		pending = err
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), pending)
		case <-ticker.C:
		}
	}
}

// Close stops observation and releases its duplicate Job and process handles.
// It does not confirm exit or reclaim ACL/profile resources. On failed recovery,
// keep the durable cleanup obligation before abandoning observation. The original
// job handle remains caller-owned; closing the last Job handle triggers its
// kill-on-close policy. Close serializes with confirmation and is idempotent.
func (c *ProcessCensus) Close() error {
	c.closeOnce.Do(func() {
		c.operation <- struct{}{}
		defer func() { <-c.operation }()
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		portClosed := false
		if err := windows.PostQueuedCompletionStatus(c.port, 0, 1, nil); err != nil {
			c.closeErr = errors.Join(c.closeErr, err, windows.CloseHandle(c.port))
			portClosed = true
		}
		<-c.done
		if !portClosed {
			c.closeErr = errors.Join(c.closeErr, windows.CloseHandle(c.port))
		}
		for _, process := range c.processes {
			c.closeErr = errors.Join(c.closeErr, windows.CloseHandle(process))
		}
		c.closeErr = errors.Join(c.closeErr, windows.CloseHandle(c.job))
	})
	return c.closeErr
}
