package winjob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsJobCensusGuards(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_EXEC") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_EXEC=1 on Windows")
	}
	t.Setenv("XMESH_WINJOB_DRAIN_HELPER", "1")
	t.Run("closed-and-cancelled", func(t *testing.T) {
		_, census := newDrainCensus(t)
		if err := census.WaitEmpty(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := census.TerminateAndWait(ctx, 1); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled census confirmation", err)
		}
		for i := 0; i < 2; i++ {
			if err := census.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := census.WaitEmpty(context.Background()); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			t.Fatal("closed census accepted", err)
		}
	})
	t.Run("nonempty-job", func(t *testing.T) {
		job := newDrainJob(t)
		process := startDrainProcess(t, job, "member", "unused", true)
		census, err := NewProcessCensus(job)
		if census != nil || !errors.Is(err, ErrIncompleteProcessSet) {
			t.Fatal("late observation accepted", census, err)
		}
		assertDrainProcessAlive(t, process.Process)
	})
	t.Run("retain-root", func(t *testing.T) {
		job, census := newDrainCensus(t)
		foreign := startDrainProcess(t, 0, "member", "unused", true)
		if err := census.RetainProcess(context.Background(), foreign.Process); !errors.Is(err, ErrIncompleteProcessSet) {
			t.Fatal("foreign root accepted", err)
		}
		process := startDrainProcess(t, job, "member", "unused", true)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := census.RetainProcess(ctx, process.Process); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled root retention", err)
		}
		var source windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), process.Process, windows.CurrentProcess(), &source, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			t.Fatal(err)
		}
		err := census.RetainProcess(context.Background(), source)
		closeErr := windows.CloseHandle(source)
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
		if err := census.RetainProcess(context.Background(), process.Process); err != nil {
			t.Fatal("duplicate root notification/retention", err)
		}
		if err := census.TerminateAndWait(context.Background(), 79); err != nil {
			t.Fatal("source handle release invalidated retained root", err)
		}
		assertDrainProcessAlive(t, foreign.Process)
	})
	t.Run("missing-notification", func(t *testing.T) {
		job, census := newDrainCensus(t)
		// Remove the association before admission to emulate a notification
		// that was never delivered. Accounting must still retain the member.
		var detached struct {
			Key  uintptr
			Port windows.Handle
		}
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectAssociateCompletionPortInformation, uintptr(unsafe.Pointer(&detached)), uint32(unsafe.Sizeof(detached))); err != nil {
			t.Fatal(err)
		}
		process := startDrainProcess(t, job, "member", "unused", true)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := census.TerminateAndWait(ctx, 1)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrIncompleteProcessSet) {
			t.Fatal("missing notification certified an incomplete census", err)
		}
		assertDrainProcessAlive(t, process.Process)
	})
}

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
			observeJobTestMembers(t, job)
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
			code, key, value, err := dequeueJobNotification(port)
			if err != nil {
				t.Log("job member event query", err)
				return
			}
			if key == 1 {
				return
			}
			pid := uint32(value)
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
