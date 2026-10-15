package winjob

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Keep a single application alive while reading its actual Job membership.
// This diagnostic distinguishes kernel-created members from application trees.
func Test_WindowsJobMemberCensus(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_EXEC") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_EXEC=1 on Windows")
	}
	t.Setenv("XMESH_WINJOB_DRAIN_HELPER", "1")
	for _, suspended := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "suspended-then-running"}[suspended], func(t *testing.T) {
			job := newDrainJob(t)
			process := startDrainProcess(t, job, "member", "unused", suspended)
			logJobCensus(t, job)
			if suspended {
				if _, err := windows.ResumeThread(process.Thread); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 4; i++ {
				time.Sleep(250 * time.Millisecond)
				logJobCensus(t, job)
			}
		})
	}
}

func logJobCensus(t *testing.T, job windows.Handle) {
	t.Helper()
	var accounting jobAccounting
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Assigned, Returned uint32
		IDs                [256]uintptr
	}
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil {
		t.Fatal(err)
	}
	if list.Assigned != list.Returned || list.Returned > uint32(len(list.IDs)) {
		t.Fatal("incomplete diagnostic process list")
	}
	t.Logf("job accounting: total=%d active=%d terminated=%d listed=%d", accounting.TotalProcesses, accounting.ActiveProcesses, accounting.TotalTerminatedProcesses, list.Returned)
	for _, id := range list.IDs[:list.Returned] {
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(id))
		if err != nil {
			t.Logf("member %d query: %v", id, err)
			continue
		}
		var path [32768]uint16
		size := uint32(len(path))
		err = windows.QueryFullProcessImageName(process, 0, &path[0], &size)
		_ = windows.CloseHandle(process)
		if err != nil {
			t.Logf("member %d image query: %v", id, err)
		} else {
			t.Logf("member %d image=%s", id, filepath.Base(windows.UTF16ToString(path[:size])))
		}
	}
}

func observeJobTestMembers(t *testing.T, job windows.Handle) {
	t.Helper()
	port, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	info := struct {
		Key  uintptr
		Port windows.Handle
	}{Key: 2, Port: port}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectAssociateCompletionPortInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(port)
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var code uint32
			var key uintptr
			var value *windows.Overlapped
			if err := windows.GetQueuedCompletionStatus(port, &code, &key, &value, windows.INFINITE); err != nil {
				t.Log("job member event query", err)
				return
			}
			if key == 1 {
				return
			}
			pid := uint32(uintptr(unsafe.Pointer(value)))
			t.Logf("job event code=%d process=%d", code, pid)
			if code != 6 { // JOB_OBJECT_MSG_NEW_PROCESS
				continue
			}
			process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil {
				t.Logf("new member %d query: %v", pid, err)
				continue
			}
			var path [32768]uint16
			size := uint32(len(path))
			err = windows.QueryFullProcessImageName(process, 0, &path[0], &size)
			_ = windows.CloseHandle(process)
			if err != nil {
				t.Logf("new member %d image query: %v", pid, err)
			} else {
				t.Logf("new member %d image=%s", pid, filepath.Base(windows.UTF16ToString(path[:size])))
			}
		}
	}()
	t.Cleanup(func() {
		if err := windows.PostQueuedCompletionStatus(port, 0, 1, nil); err != nil {
			t.Error("stop member observer", err)
			_ = windows.CloseHandle(port)
			<-done
			return
		}
		<-done
		_ = windows.CloseHandle(port)
	})
}
