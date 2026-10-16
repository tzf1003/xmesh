package winbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type stdioProof struct {
	Input        string
	AppContainer bool
}

func stdioHandleFlags(t *testing.T, handle windows.Handle) uint32 {
	t.Helper()
	var flags uint32
	result, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation").Call(uintptr(handle), uintptr(unsafe.Pointer(&flags)))
	if result == 0 {
		t.Fatal("standard stream handle unavailable", err)
	}
	return flags
}

type stdioReadResult struct {
	body []byte
	err  error
}

func drainExecutionTestStream(file *os.File) <-chan stdioReadResult {
	result := make(chan stdioReadResult, 1)
	go func() {
		body, err := io.ReadAll(io.LimitReader(file, 64*1024))
		if err == nil {
			_, err = io.Copy(io.Discard, file)
		}
		result <- stdioReadResult{body, err}
	}()
	return result
}

func awaitExecutionTestStream(t *testing.T, result <-chan stdioReadResult) []byte {
	t.Helper()
	select {
	case read := <-result:
		if read.err != nil {
			t.Fatal("standard stream drain", read.err)
		}
		return read.body
	case <-time.After(5 * time.Second):
		t.Fatal("standard stream drain deadline")
		return nil
	}
}

func Test_WindowsAppContainerProcessStdio(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"pipes", "shared-file", "supervised"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			base := t.TempDir()
			state := filepath.Join(base, "state")
			privateJournalDirectory(t, state)
			journal, err := openBoundGrantJournal(ctx, filepath.Join(state, "stdio.journal"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			p, err := newProfile(journal)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := p.Close(); err != nil {
					t.Error("stdio profile cleanup", err)
				}
			}()
			var owner *executionProcesses
			if mode == "supervised" {
				owner, err = newSupervisedExecutionProcesses(ctx, p, 1, supervisorTestCommand(t, false))
			} else {
				owner, err = newExecutionProcesses(p, 1)
			}
			if owner != nil {
				defer func() {
					if err := owner.reap(context.Background(), 91); err != nil {
						t.Error("stdio execution cleanup", err)
					}
					if err := owner.Close(); err != nil {
						t.Error(err)
					}
				}()
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(p.Directory(), "stdio-helper.exe")
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			secret, err := os.Create(filepath.Join(base, "unrelated.txt"))
			if err != nil {
				t.Fatal(err)
			}
			defer secret.Close()
			secretHandle := windows.Handle(secret.Fd())
			if err := windows.SetHandleInformation(secretHandle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
				t.Fatal(err)
			}
			var secretInfo windows.ByHandleFileInformation
			if err := windows.GetFileInformationByHandle(secretHandle, &secretInfo); err != nil {
				t.Fatal(err)
			}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			text := "stdin 日本 😀\nsecond line\x00binary"
			var output, errorOutput, reader, errorReader *os.File
			if mode == "shared-file" {
				output, err = os.Create(filepath.Join(base, "spool.txt"))
				errorOutput = output
			} else {
				reader, output, err = os.Pipe()
				if err == nil {
					errorReader, errorOutput, err = os.Pipe()
				}
			}
			for _, file := range []*os.File{output, errorOutput, reader, errorReader} {
				if file != nil {
					defer file.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			stdio := &executionStdio{windows.Handle(input.Fd()), windows.Handle(output.Fd()), windows.Handle(errorOutput.Fd())}
			flags := []uint32{stdioHandleFlags(t, stdio.stdin), stdioHandleFlags(t, stdio.stdout), stdioHandleFlags(t, stdio.stderr)}
			var stdoutResult, stderrResult <-chan stdioReadResult
			if mode != "shared-file" {
				stdoutResult, stderrResult = drainExecutionTestStream(reader), drainExecutionTestStream(errorReader)
			}
			environment := []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_STDIO_HELPER=1", "GORACE=log_path=stdio-race"}
			process, err := owner.startWithStdio(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerStdioHelper$"}, p.Directory(), environment, stdio)
			if err != nil {
				t.Fatal("stdio launch", err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			for index, handle := range []windows.Handle{stdio.stdin, stdio.stdout, stdio.stderr} {
				if stdioHandleFlags(t, handle) != flags[index] {
					t.Fatal("stdio launch changed original handle flags")
				}
			}
			// Probe from the unrestricted owner while the helper is waiting for
			// stdin. Invalid handle probes in an AppContainer can raise a kernel
			// exception instead of returning a Win32 error.
			var probe windows.Handle
			if err := windows.DuplicateHandle(process.Process, secretHandle, windows.CurrentProcess(), &probe, 0, false, windows.DUPLICATE_SAME_ACCESS); err == nil {
				kind, kindErr := windows.GetFileType(probe)
				var info windows.ByHandleFileInformation
				inherited := kindErr == nil && kind == windows.FILE_TYPE_DISK && windows.GetFileInformationByHandle(probe, &info) == nil &&
					info.VolumeSerialNumber == secretInfo.VolumeSerialNumber && info.FileIndexHigh == secretInfo.FileIndexHigh && info.FileIndexLow == secretInfo.FileIndexLow
				_ = windows.CloseHandle(probe)
				if inherited {
					t.Fatal("unrelated file capability inherited")
				}
			} else if !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
				t.Fatal("child handle isolation probe", err)
			}
			if _, err := io.WriteString(writer, text); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if mode != "shared-file" {
				_ = output.Close()
				_ = errorOutput.Close()
			}
			if result, err := windows.WaitForSingleObject(process.Process, 15000); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("stdio process wait", result, err)
			}
			var exit uint32
			if err := windows.GetExitCodeProcess(process.Process, &exit); err != nil {
				t.Fatal("stdio process exit query", err)
			}
			var stdout, stderr []byte
			if mode == "shared-file" {
				if _, err := output.Seek(0, io.SeekStart); err != nil {
					t.Fatal("original output handle lost", err)
				}
				body, err := io.ReadAll(output)
				if err != nil {
					t.Fatal(err)
				}
				stdout, stderr, _ = bytes.Cut(body, []byte("\n"))
			} else {
				stdout, stderr = awaitExecutionTestStream(t, stdoutResult), awaitExecutionTestStream(t, stderrResult)
			}
			if exit != 0 {
				t.Fatalf("stdio process exit %d: stdout=%s stderr=%s", exit, stdout, stderr)
			}
			var proof stdioProof
			if err := json.Unmarshal(stdout, &proof); err != nil || proof.Input != text || !proof.AppContainer || string(stderr) != "stderr 日本 😀\n" {
				t.Fatal("stdio capability proof", proof, string(stdout), string(stderr), err)
			}
		})
	}
}

func Test_WindowsAppContainerStdioHelper(t *testing.T) {
	if os.Getenv("XMESH_STDIO_HELPER") != "1" {
		t.Skip("AppContainer standard stream helper")
	}
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(stdioProof{string(body), container != 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stderr, "stderr 日本 😀\n"); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func Test_WindowsAppContainerStdioAdmission(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	owner, err := newExecutionProcesses(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.reap(context.Background(), 0); _ = owner.Close() }()
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	handle := windows.Handle(file.Fd())
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	for _, invalid := range []windows.Handle{0, windows.InvalidHandle, event} {
		for index := 0; index < 3; index++ {
			handles := []windows.Handle{handle, handle, handle}
			handles[index] = invalid
			_, err := owner.startWithStdio(context.Background(), []string{"unlaunched.exe"}, p.Directory(), nil, &executionStdio{handles[0], handles[1], handles[2]})
			if err == nil || !strings.Contains(err.Error(), "standard stream") || len(owner.jobs) != 0 {
				t.Fatal("invalid stdio changed execution admission", index, err)
			}
			_ = stdioHandleFlags(t, handle)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.startWithStdio(ctx, []string{"unlaunched.exe"}, p.Directory(), nil, &executionStdio{handle, handle, handle}); !errors.Is(err, context.Canceled) || len(owner.jobs) != 0 {
		t.Fatal("cancelled stdio changed execution admission", err)
	}
	if _, err := owner.startWithStdio(context.Background(), []string{filepath.Join(p.Directory(), "unlaunched.exe")}, p.Directory(), nil, &executionStdio{handle, handle, handle}); err == nil || len(owner.jobs) != 1 {
		t.Fatal("failed creation lost its Job obligation", err, len(owner.jobs))
	}
	_ = stdioHandleFlags(t, handle)
}
