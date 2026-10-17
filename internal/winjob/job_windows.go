// Package winjob binds a Windows process to its lifetime Job during creation.
package winjob

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const jobListAttribute = 0x0002000d

// New creates a non-inheritable job that kills its process tree when the last
// handle closes, including when the hosting Agent terminates abnormally.
func New() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// Apply assigns the new process to job as part of CreateProcess. The caller
// owns the job handle and must keep it open through creation. The attribute
// list retains the handle array until Delete. Requires Windows 10 / Server 2016.
func Apply(attributes *windows.ProcThreadAttributeListContainer, job windows.Handle) error {
	if attributes == nil || job == 0 || job == windows.InvalidHandle {
		return fmt.Errorf("process job or attribute list unavailable")
	}
	jobs := &[1]windows.Handle{job}
	return attributes.Update(jobListAttribute, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0]))
}

// JobListDenied reports whether a CreateProcess failure that used Apply may be
// retried with AssignAfterCreate. Some targets, such as a Microsoft Store
// application execution alias under WindowsApps, are launched through a broker
// that refuses PROC_THREAD_ATTRIBUTE_JOB_LIST with ERROR_ACCESS_DENIED even
// though the same image starts without it.
func JobListDenied(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// AssignAfterCreate is the fallback for JobListDenied. The process must have
// been created with CREATE_SUSPENDED and without the job-list attribute: it has
// then executed no instruction, so binding it here, before its primary thread
// is resumed, leaves it no window to run outside the Job. Membership is verified
// before returning; on any error the caller must terminate the still suspended
// process instead of resuming it.
func AssignAfterCreate(job, process windows.Handle) error {
	if job == 0 || job == windows.InvalidHandle || process == 0 {
		return fmt.Errorf("process job or process handle unavailable")
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fmt.Errorf("assign suspended process to job: %w", err)
	}
	var member int32
	if ok, _, callErr := isProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&member))); ok == 0 {
		return fmt.Errorf("verify process job membership: %w", callErr)
	}
	if member == 0 {
		return fmt.Errorf("process is not a member of its job after assignment")
	}
	return nil
}
