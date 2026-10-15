package pty

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/policy"
	"golang.org/x/sys/windows"
)

type windowsOutput struct {
	mu   sync.Mutex
	text strings.Builder
	done chan struct{}
}

func nativeTerminal(t *testing.T) (*windowsTerminal, *windowsOutput) {
	t.Helper()
	if os.Getenv("XMESH_TEST_WINDOWS_PTY") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_PTY=1 on Windows with ConPTY and PowerShell")
	}
	shell, err := defaultShell()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	env, err := engine.Environment(nil)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := start(shell, []string{"-NoLogo", "-NoProfile", "-NoExit", "-Command", "Write-Output ('TERMINAL_'+'READY')"}, t.TempDir(), env, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	term := terminal.(*windowsTerminal)
	output := &windowsOutput{done: make(chan struct{})}
	go func() {
		defer close(output.done)
		buffer := make([]byte, 32768)
		for {
			n, err := term.Read(buffer)
			if n > 0 {
				output.mu.Lock()
				output.text.Write(buffer[:n])
				output.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = term.Signal("KILL")
		select {
		case <-term.exited:
		case <-time.After(10 * time.Second):
			t.Error("ConPTY process survived cleanup")
		}
		_ = term.Close()
		select {
		case <-output.done:
		case <-time.After(10 * time.Second):
			t.Error("ConPTY reader survived cleanup")
		}
	})
	output.until(t, `TERMINAL_READY`)
	return term, output
}

func (o *windowsOutput) until(t *testing.T, pattern string) []string {
	return o.untilAfter(t, 0, pattern)
}

func (o *windowsOutput) mark() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.text.Len()
}

func (o *windowsOutput) untilAfter(t *testing.T, offset int, pattern string) []string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	var text string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		o.mu.Lock()
		text = o.text.String()[offset:]
		o.mu.Unlock()
		if match := re.FindStringSubmatch(text); match != nil {
			return match
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ConPTY output deadline for %s: %q", pattern, text)
	return nil
}

func windowsWrite(t *testing.T, term *windowsTerminal, command string) {
	t.Helper()
	if _, err := term.Write([]byte(command + "\r")); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsPTYInterruptAndResize(t *testing.T) {
	term, output := nativeTerminal(t)
	if err := term.Resize(101, 37); err != nil {
		t.Fatal(err)
	}
	windowsWrite(t, term, "Write-Output ('WINDOW_'+[Console]::WindowWidth+'x'+[Console]::WindowHeight)")
	output.until(t, `WINDOW_101x37`)
	windowsWrite(t, term, "Write-Output ('SLEEP_'+'READY'); Start-Sleep 300; Write-Output ('SLEEP_'+'FINISHED')")
	output.until(t, `SLEEP_READY`)
	interrupted := output.mark()
	if err := term.Signal("INT"); err != nil {
		t.Fatal(err)
	}
	output.untilAfter(t, interrupted, `PS [^\r\n]*>`)
	windowsWrite(t, term, "Write-Output ('INTERRUPT_'+'SURVIVED')")
	output.until(t, `INTERRUPT_SURVIVED`)
}

func Test_WindowsPTYProcessTreeCleanup(t *testing.T) {
	for _, action := range []string{"TERM", "KILL", "shell-exit"} {
		t.Run(action, func(t *testing.T) {
			term, output := nativeTerminal(t)
			windowsWrite(t, term, `$child = Start-Process -FilePath (Get-Process -Id $PID).Path -ArgumentList '-NoLogo','-NoProfile','-NonInteractive','-Command','Start-Sleep 300' -NoNewWindow -PassThru; Write-Output ('CHILD_'+$child.Id)`)
			childID, err := strconv.Atoi(output.until(t, `CHILD_([0-9]+)`)[1])
			if err != nil {
				t.Fatal(err)
			}
			child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(childID))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = windows.TerminateProcess(child, 1); _ = windows.CloseHandle(child) })
			if action == "shell-exit" {
				windowsWrite(t, term, "Write-Output ('FINAL_'+'DRAIN'); exit")
			} else if err := term.Signal(action); err != nil {
				t.Fatal(err)
			}
			select {
			case <-term.exited:
			case <-time.After(10 * time.Second):
				t.Fatal("root process survived", action)
			}
			status, err := windows.WaitForSingleObject(child, 10000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("background process survived", action, status, err)
			}
			select {
			case <-output.done:
			case <-time.After(10 * time.Second):
				t.Fatal("console did not drain", action)
			}
			if action == "shell-exit" {
				output.until(t, `FINAL_DRAIN`)
			}
			if err := term.Wait(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := term.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func Test_WindowsPTYStartupFailure(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_PTY") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_PTY=1 on Windows")
	}
	for i := 0; i < 3; i++ {
		root := t.TempDir()
		term, err := start(filepath.Join(root, "missing.exe"), nil, root, os.Environ(), 80, 24, false)
		if err == nil || term != nil {
			t.Fatal("missing executable accepted", term, err)
		}
	}
}
