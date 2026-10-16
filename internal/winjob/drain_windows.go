package winjob

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION has the same 48-byte layout on
// Windows amd64 and arm64. ActiveProcesses includes suspended members and
// descendants that remain after the initial process exits.
type jobAccounting struct {
	TotalUserTime, TotalKernelTime                     int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses                uint32
	ActiveProcesses, TotalTerminatedProcesses          uint32
}

// ErrIncompleteProcessSet retains the cleanup obligation when the supplied
// lifetime handle census differs from the kernel's total assigned membership.
var ErrIncompleteProcessSet = errors.New("job process handle census is incomplete")

// WaitEmpty confirms zero active accounting and signalled lifetime handles for
// every process ever assigned to job, including exited roots and descendants.
// The trusted caller must establish each handle's membership, retain it from
// admission until reclamation, and prevent all new admissions (including child
// creation) through reclamation. Handles need SYNCHRONIZE and query-limited
// access. The total assigned count must equal the distinct retained PID count;
// a transient untracked descendant therefore retains an error even after exit.
// This proves only the supplied job, not other jobs sharing a profile. The wait
// is cancellable and limited to 30 seconds. Errors retain caller-owned handles.
func WaitEmpty(ctx context.Context, job windows.Handle, processes ...windows.Handle) error {
	if job == 0 || job == windows.InvalidHandle {
		return windows.ERROR_INVALID_HANDLE
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := validateProcessHandles(processes); err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := queryProcessAccounting(job, len(processes))
		if err != nil {
			return err
		}
		allExited := true
		for _, process := range processes {
			result, err := windows.WaitForSingleObject(process, 0)
			if err != nil {
				return fmt.Errorf("query retained process exit: %w", err)
			}
			switch result {
			case windows.WAIT_OBJECT_0:
			case uint32(windows.WAIT_TIMEOUT):
				allExited = false
			default:
				return fmt.Errorf("unexpected retained process wait result: %d", result)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.ActiveProcesses == 0 && allExited {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// TerminateAndWait checks the trusted lifetime handle census before requesting
// termination, then confirms accounting and every retained process exit using
// WaitEmpty. The same census and admission constraints apply. Cancellation after
// termination does not undo that request; retain all handles for retry on error.
func TerminateAndWait(ctx context.Context, job windows.Handle, exitCode uint32, processes ...windows.Handle) error {
	if job == 0 || job == windows.InvalidHandle {
		return windows.ERROR_INVALID_HANDLE
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProcessHandles(processes); err != nil {
		return err
	}
	if _, err := queryProcessAccounting(job, len(processes)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.TerminateJobObject(job, exitCode); err != nil {
		return fmt.Errorf("terminate job process tree: %w", err)
	}
	return WaitEmpty(ctx, job, processes...)
}

func validateProcessHandles(processes []windows.Handle) error {
	if len(processes) > 10000 {
		return fmt.Errorf("%w: more than 10000 retained handles", ErrIncompleteProcessSet)
	}
	ids := make(map[uint32]bool, len(processes))
	for _, process := range processes {
		if process == 0 || process == windows.InvalidHandle {
			return windows.ERROR_INVALID_HANDLE
		}
		pid, err := windows.GetProcessId(process)
		if err != nil {
			return fmt.Errorf("query retained process identity: %w", err)
		}
		if ids[pid] {
			return fmt.Errorf("%w: duplicate process identity", ErrIncompleteProcessSet)
		}
		ids[pid] = true
	}
	return nil
}

func queryProcessAccounting(job windows.Handle, retained int) (jobAccounting, error) {
	var info jobAccounting
	var size uint32
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &size); err != nil {
		return info, fmt.Errorf("query job process accounting: %w", err)
	}
	if size != uint32(unsafe.Sizeof(info)) {
		return info, fmt.Errorf("job process accounting returned %d bytes", size)
	}
	if info.TotalProcesses != uint32(retained) {
		return info, fmt.Errorf("%w: assigned=%d retained=%d", ErrIncompleteProcessSet, info.TotalProcesses, retained)
	}
	return info, nil
}
