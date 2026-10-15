// Package winjob binds a Windows process to its lifetime Job during creation.
package winjob

import (
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
