package winjob

import (
	"context"
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

// WaitEmpty confirms that the kernel reports no active processes in job. The
// caller must own the live handle and prevent further process admission for the
// entire wait and any subsequent resource reclamation. A successful observation
// applies only to this job; it does not enumerate other jobs using a profile.
// The wait is cancellable and limited to 30 seconds even without a deadline.
// Errors retain the caller's handle and all cleanup obligations.
func WaitEmpty(ctx context.Context, job windows.Handle) error {
	if job == 0 || job == windows.InvalidHandle {
		return windows.ERROR_INVALID_HANDLE
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var info jobAccounting
		var size uint32
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &size); err != nil {
			return fmt.Errorf("query job process accounting: %w", err)
		}
		if size != uint32(unsafe.Sizeof(info)) {
			return fmt.Errorf("job process accounting returned %d bytes", size)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// TerminateAndWait requests termination of every job member, then confirms an
// empty job with WaitEmpty. The caller must prevent new admission and retain the
// handle through resource reclamation. Cancellation after termination does not
// undo that request; a failed confirmation can be retried with the same handle.
func TerminateAndWait(ctx context.Context, job windows.Handle, exitCode uint32) error {
	if job == 0 || job == windows.InvalidHandle {
		return windows.ERROR_INVALID_HANDLE
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.TerminateJobObject(job, exitCode); err != nil {
		return fmt.Errorf("terminate job process tree: %w", err)
	}
	return WaitEmpty(ctx, job)
}
