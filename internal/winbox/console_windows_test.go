package winbox

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type executionTestConsole struct {
	handle        windows.Handle
	input, output *os.File
	mu            sync.Mutex
	text          strings.Builder
	done          chan struct{}
}

func newExecutionTestConsole(t *testing.T) *executionTestConsole {
	t.Helper()
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inR)
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		_ = windows.CloseHandle(inW)
		t.Fatal(err)
	}
	defer windows.CloseHandle(outW)
	c := &executionTestConsole{input: os.NewFile(uintptr(inW), "execution-console-input"), output: os.NewFile(uintptr(outR), "execution-console-output"), done: make(chan struct{})}
	if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 24}, inR, outW, 0, &c.handle); err != nil {
		_ = c.input.Close()
		_ = c.output.Close()
		t.Fatal(err)
	}
	go func() {
		defer close(c.done)
		buffer := make([]byte, 4096)
		for {
			n, err := c.output.Read(buffer)
			c.mu.Lock()
			if available := 64*1024 - c.text.Len(); available > 0 {
				if n > available {
					n = available
				}
				c.text.Write(buffer[:n])
			}
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *executionTestConsole) until(t *testing.T, expression string) []string {
	t.Helper()
	re := regexp.MustCompile(expression)
	var text string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		c.mu.Lock()
		text = c.text.String()
		c.mu.Unlock()
		if match := re.FindStringSubmatch(text); match != nil {
			return match
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("AppContainer pseudoconsole output deadline: %q", text)
	return nil
}

func (c *executionTestConsole) close(t *testing.T) {
	t.Helper()
	// The reader remains active while ClosePseudoConsole flushes output.
	windows.ClosePseudoConsole(c.handle)
	_ = c.input.Close()
	_ = c.output.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Error("AppContainer pseudoconsole reader survived cleanup")
	}
}

func Test_WindowsAppContainerProcessConsole(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []struct{ supervised, terminate bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("supervised=%t/terminate=%t", mode.supervised, mode.terminate), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			state := filepath.Join(t.TempDir(), "state")
			privateJournalDirectory(t, state)
			journal, err := openBoundGrantJournal(ctx, filepath.Join(state, "console.journal"), true)
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
					t.Error(err)
				}
			}()
			c := newExecutionTestConsole(t)
			defer c.close(t)
			var owner *executionProcesses
			if mode.supervised {
				owner, err = newSupervisedExecutionProcesses(ctx, p, 1, supervisorTestCommand(t, false))
			} else {
				owner, err = newExecutionProcesses(p, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := owner.reap(context.Background(), 93); err != nil {
					t.Error("console execution cleanup", err)
				}
				if err := owner.Close(); err != nil {
					t.Error(err)
				}
			}()
			body, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(p.Directory(), "console-helper.exe")
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			process, err := owner.startWithConsole(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerConsoleHelper$"}, p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_CONSOLE_HELPER=1", "GORACE=log_path=console-race"}, c.handle)
			if err != nil {
				t.Fatal("console launch", err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			c.until(t, "CONSOLE_READY")
			if mode.terminate {
				if err := owner.reap(ctx, 93); err != nil {
					t.Fatal("blocked console execution reap", err)
				}
				var exit uint32
				if err := windows.GetExitCodeProcess(process.Process, &exit); err != nil || exit != 93 {
					t.Fatal("terminated console process exit", exit, err)
				}
				if err := requireJournalProcessesReaped(journal); err != nil {
					t.Fatal("terminated console durable confirmation", err)
				}
				return
			}
			if err := windows.ResizePseudoConsole(c.handle, windows.Coord{X: 137, Y: 43}); err != nil {
				t.Fatal("caller pseudoconsole lost", err)
			}
			input := "input 日本 😀"
			if _, err := io.WriteString(c.input, input+"\r"); err != nil {
				t.Fatal(err)
			}
			match := c.until(t, `CONSOLE_PROOF app=1 size=137x43 input=([A-Za-z0-9+/=]+)`)
			decoded, err := base64.StdEncoding.DecodeString(match[1])
			if err != nil || string(decoded) != input {
				t.Fatal("console input", string(decoded), err)
			}
			if result, err := windows.WaitForSingleObject(process.Process, 10000); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("console exit wait", result, err)
			}
			var exit uint32
			if err := windows.GetExitCodeProcess(process.Process, &exit); err != nil || exit != 0 {
				t.Fatal("console process exit", exit, err)
			}
			if err := owner.reap(ctx, 0); err != nil {
				t.Fatal("console identity confirmation", err)
			}
			if err := requireJournalProcessesReaped(journal); err != nil {
				t.Fatal("console durable confirmation", err)
			}
		})
	}
}

func Test_WindowsAppContainerConsoleHelper(t *testing.T) {
	if os.Getenv("XMESH_CONSOLE_HELPER") != "1" {
		t.Skip("AppContainer pseudoconsole helper")
	}
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil {
		t.Fatal(err)
	}
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetConsoleMode(windows.Handle(os.Stdin.Fd()), mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "CONSOLE_READY")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info); err != nil {
		t.Fatal(err)
	}
	input := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	fmt.Fprintf(os.Stdout, "CONSOLE_PROOF app=%d size=%dx%d input=%s\n", container, info.Window.Right-info.Window.Left+1, info.Window.Bottom-info.Window.Top+1, base64.StdEncoding.EncodeToString([]byte(input)))
	os.Exit(0)
}

func Test_WindowsAppContainerConsoleAdmission(t *testing.T) {
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
	for _, invalid := range []windows.Handle{0, windows.InvalidHandle} {
		if _, err := owner.startWithConsole(context.Background(), []string{"unlaunched.exe"}, p.Directory(), nil, invalid); err == nil || len(owner.jobs) != 0 {
			t.Fatal("invalid console changed admission", err)
		}
	}
	c := newExecutionTestConsole(t)
	defer c.close(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.startWithConsole(ctx, []string{"unlaunched.exe"}, p.Directory(), nil, c.handle); !errors.Is(err, context.Canceled) || len(owner.jobs) != 0 {
		t.Fatal("cancelled console changed admission", err)
	}
	if _, err := owner.startWithConsole(context.Background(), []string{filepath.Join(p.Directory(), "unlaunched.exe")}, p.Directory(), nil, c.handle); err == nil || len(owner.jobs) != 1 {
		t.Fatal("failed console launch lost Job obligation", err, len(owner.jobs))
	}
	if err := windows.ResizePseudoConsole(c.handle, windows.Coord{X: 81, Y: 25}); err != nil {
		t.Fatal("failed launch released caller console", err)
	}
}
