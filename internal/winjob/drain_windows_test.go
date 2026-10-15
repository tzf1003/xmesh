package winjob

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsJobDrain(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_EXEC") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_EXEC=1 on Windows")
	}
	t.Setenv("XMESH_WINJOB_DRAIN_HELPER", "1")
	t.Run("exited-root-live-descendant", func(t *testing.T) {
		job := newDrainJob(t)
		proof := filepath.Join(t.TempDir(), "descendant.json")
		root := startDrainProcess(t, job, "root", proof, false)
		var pid uint32
		for deadline := time.Now().Add(15 * time.Second); ; {
			body, err := os.ReadFile(proof)
			if err == nil && json.Unmarshal(body, &pid) == nil && pid != 0 {
				break
			}
			assertDrainProcessAlive(t, root.Process)
			if time.Now().After(deadline) {
				t.Fatal("root did not publish retained descendant")
			}
			time.Sleep(10 * time.Millisecond)
		}
		child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(child)
		logJobCensus(t, job)
		// The root retains its child handle until this acknowledgement. The
		// controller now owns an overlapping lifetime handle before root exit.
		if err := os.WriteFile(proof+".captured", []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		if result, err := windows.WaitForSingleObject(root.Process, 15000); err != nil || result != windows.WAIT_OBJECT_0 {
			t.Fatal("root did not exit", result, err)
		}
		var exit uint32
		if err := windows.GetExitCodeProcess(root.Process, &exit); err != nil || exit != 0 {
			t.Fatal("root helper failed", exit, err)
		}
		control := startDrainProcess(t, 0, "member", "unused", false)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := WaitEmpty(ctx, job, root.Process, child); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("live descendant was not retained", err)
		}
		assertDrainProcessAlive(t, child)
		if err := TerminateAndWait(context.Background(), job, 73, root.Process, child); err != nil {
			t.Fatal(err)
		}
		if result, err := windows.WaitForSingleObject(child, 0); err != nil || result != windows.WAIT_OBJECT_0 {
			t.Fatal("empty observation preceded descendant exit", result, err)
		}
		if err := windows.GetExitCodeProcess(child, &exit); err != nil || exit != 73 {
			t.Fatal("descendant termination code", exit, err)
		}
		assertDrainProcessAlive(t, control.Process)
		if err := WaitEmpty(context.Background(), job, child); !errors.Is(err, ErrIncompleteProcessSet) {
			t.Fatal("omitted exited root accepted", err)
		}
		if err := WaitEmpty(context.Background(), job, root.Process, child); err != nil {
			t.Fatal("empty observation retry", err)
		}
		if err := TerminateAndWait(context.Background(), job, 73, root.Process, child); err != nil {
			t.Fatal("termination retry", err)
		}
	})
	t.Run("suspended-cancel-retry", func(t *testing.T) {
		job := newDrainJob(t)
		process := startDrainProcess(t, job, "member", "unused", true)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := TerminateAndWait(ctx, job, 74, process.Process); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled termination", err)
		}
		assertDrainProcessAlive(t, process.Process)
		if err := TerminateAndWait(context.Background(), job, 74); !errors.Is(err, ErrIncompleteProcessSet) {
			t.Fatal("missing lifetime member accepted", err)
		}
		assertDrainProcessAlive(t, process.Process)
		if err := WaitEmpty(context.Background(), job, process.Process, process.Process); !errors.Is(err, ErrIncompleteProcessSet) {
			t.Fatal("duplicate lifetime member accepted", err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := WaitEmpty(ctx, job, process.Process); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("suspended member was not counted", err)
		}
		assertDrainProcessAlive(t, process.Process)
		if err := TerminateAndWait(context.Background(), job, 74, process.Process); err != nil {
			t.Fatal(err)
		}
		if result, err := windows.WaitForSingleObject(process.Process, 0); err != nil || result != windows.WAIT_OBJECT_0 {
			t.Fatal("suspended member exit", result, err)
		}
	})
	t.Run("handles-and-cancellation", func(t *testing.T) {
		for _, h := range []windows.Handle{0, windows.InvalidHandle} {
			if err := WaitEmpty(context.Background(), h); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
				t.Fatal("invalid wait handle", err)
			}
			if err := TerminateAndWait(context.Background(), h, 1); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
				t.Fatal("invalid termination handle", err)
			}
		}
		job := newDrainJob(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := WaitEmpty(ctx, job); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled empty observation", err)
		}
		var limited windows.Handle
		const jobObjectTerminate = 0x0008
		if err := windows.DuplicateHandle(windows.CurrentProcess(), job, windows.CurrentProcess(), &limited, jobObjectTerminate, false, 0); err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(limited)
		if err := WaitEmpty(context.Background(), limited); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatal("accounting query failure accepted", err)
		}
		if err := WaitEmpty(context.Background(), job); err != nil {
			t.Fatal("query error changed caller job", err)
		}
	})
}

func newDrainJob(t *testing.T) windows.Handle {
	t.Helper()
	job, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(job) })
	observeJobTestMembers(t, job)
	return job
}

func assertDrainProcessAlive(t *testing.T, process windows.Handle) {
	t.Helper()
	if result, err := windows.WaitForSingleObject(process, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("process unexpectedly exited", result, err)
	}
}

func startDrainProcess(t *testing.T, job windows.Handle, mode, proof string, suspended bool) windows.ProcessInformation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	if job != 0 {
		if err := Apply(attributes, job); err != nil {
			t.Fatal(err)
		}
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{executable, "-test.run=^Test_WindowsJobDrainHelper$", "--", mode, proof}))
	if err != nil {
		t.Fatal(err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attributes.List()}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if suspended {
		flags |= windows.CREATE_SUSPENDED
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcess(nil, line, nil, nil, false, flags, nil, nil, &startup.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(process.Process, 1)
		_, _ = windows.WaitForSingleObject(process.Process, 10000)
		_ = windows.CloseHandle(process.Thread)
		_ = windows.CloseHandle(process.Process)
	})
	return process
}

func Test_WindowsJobDrainHelper(t *testing.T) {
	if os.Getenv("XMESH_WINJOB_DRAIN_HELPER") != "1" {
		t.Skip("job drain helper")
	}
	args := os.Args[len(os.Args)-2:]
	if args[0] == "root" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(executable, "-test.run=^Test_WindowsJobDrainHelper$", "--", "member", "unused")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(uint32(child.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(args[1], body, 0600); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(15 * time.Second); ; {
			if _, err := os.Stat(args[1] + ".captured"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("controller did not capture descendant")
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = child.Process.Release()
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}
