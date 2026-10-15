//go:build windows

package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"golang.org/x/sys/windows"
)

func windowsExecRuntime(t *testing.T) string {
	t.Helper()
	if os.Getenv("XMESH_TEST_WINDOWS_EXEC") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_EXEC=1 on Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func helperArgs(mode string, paths ...string) []string {
	return append([]string{"-test.run=^Test_WindowsExecHelper$", "--", "--xmesh-exec-helper", mode}, paths...)
}

func Test_WindowsExecSuspendedAdmission(t *testing.T) {
	exe := windowsExecRuntime(t)
	marker := filepath.Join(t.TempDir(), "started")
	cmd := osexec.Command(exe, helperArgs("marker", marker)...)
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command ran before Job admission", err)
	}
	finish, err := track(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer finish()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	finish()
	finish()
	if b, err := os.ReadFile(marker); err != nil || string(b) != "started" {
		t.Fatal("admitted command did not execute", string(b), err)
	}
	if err := resumeSuspendedProcess(^uint32(0)); err == nil {
		t.Fatal("accepted missing process")
	}
}

func Test_WindowsExecPowerShell(t *testing.T) {
	windowsExecPowerShell(t)
}

func Test_WindowsExecPowerShellDefaultModules(t *testing.T) {
	windowsExecRuntime(t)
	for _, mode := range []string{"empty", "unset"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PSModulePath", "")
			if mode == "unset" {
				if err := os.Unsetenv("PSModulePath"); err != nil {
					t.Fatal(err)
				}
			}
			windowsExecPowerShell(t)
		})
	}
}

func windowsExecPowerShell(t *testing.T) {
	t.Helper()
	windowsExecRuntime(t)
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	env, err := p.Environment(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, shellName := range []string{"powershell", "pwsh"} {
		shell, lookupErr := osexec.LookPath(shellName)
		if lookupErr != nil {
			continue
		}
		for _, tracked := range []bool{false, true} {
			name := "standard-launch"
			if tracked {
				name = "job-launch"
			}
			name = shellName + "/" + name
			t.Run(name, func(t *testing.T) {
				cmd := osexec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", "Write-Output hello")
				cmd.Dir, cmd.Env, cmd.Stdin = t.TempDir(), env, strings.NewReader("")

				var output bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &output
				if tracked {
					prepare(cmd)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				if tracked {
					finish, err := track(cmd)
					if err != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						t.Fatal(err)
					}
					defer finish()
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					if !tracked {
						diagnoseWindowsModuleEnvironment(t, shell, env)
					}
					t.Fatalf("PowerShell launch did not complete; output: %q", output.String())
				}
				if err != nil || strings.TrimSpace(output.String()) != "hello" {
					t.Fatal("PowerShell output", output.String(), err)
				}
			})
		}
	}
}

// Compare module paths and variable casing using the same filtered environment.
// Diagnostics report labels and status; environment values stay in the child.
func diagnoseWindowsModuleEnvironment(t *testing.T, shell string, env []string) {
	t.Helper()
	base := make([]string, 0, len(env))
	var system string
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PSModulePath") {
			system = value
		} else {
			base = append(base, entry)
		}
	}
	type candidate struct{ label, key, value string }
	candidates := []candidate{
		{"system-canonical-name", "PSModulePath", system},
		{"inherited-uppercase-name", "PSMODULEPATH", os.Getenv("PSModulePath")},
		{"inherited-canonical-name", "PSModulePath", os.Getenv("PSModulePath")},
	}
	for index, entry := range filepath.SplitList(os.Getenv("PSModulePath")) {
		candidates = append(candidates, candidate{"inherited-entry-" + strconv.Itoa(index), "PSModulePath", entry})
	}
	for _, candidate := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := osexec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", "Write-Output hello")
		cmd.Env = append(append([]string(nil), base...), candidate.key+"="+candidate.value)
		cmd.Dir, cmd.Stdin, cmd.WaitDelay = t.TempDir(), strings.NewReader(""), time.Second
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		err := cmd.Run()
		cancel()
		t.Logf("module environment %s: status=%v hello=%t", candidate.label, err, strings.TrimSpace(output.String()) == "hello")
	}
}

func Test_WindowsExecProcessTreeCleanup(t *testing.T) {
	exe := windowsExecRuntime(t)
	for _, action := range []string{"root-exit", "timeout"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			pidPath, release := filepath.Join(root, "child.pid"), filepath.Join(root, "exit")
			r, err := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := policy.New(config.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			m, err := New(config.Exec{MaxConcurrent: 1, ExclusiveSlots: 1, DefaultTimeout: "20s", IdempotencyCache: 10, InlineOutputBytes: "8B"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var result Object
			var runErr error
			raw, _ := json.Marshal(Object{"cmd": exe, "args": helperArgs("tree", pidPath, release), "stdin": strings.Repeat("x", 4<<20)})
			go func() {
				defer close(done)
				result, runErr = m.Run(ctx, "n/a", r, p, raw)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
					m.Close()
				case <-time.After(10 * time.Second):
					t.Error("execution survived cleanup")
				}
			})
			var child windows.Handle
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
				if b, err := os.ReadFile(pidPath); err == nil {
					if pid, err := strconv.ParseUint(string(b), 10, 32); err == nil {
						child, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
						if err != nil {
							t.Fatal(err)
						}
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if child == 0 {
				t.Fatal("child was not started")
			}
			t.Cleanup(func() { _ = windows.TerminateProcess(child, 1); _ = windows.CloseHandle(child) })
			if action == "root-exit" {
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("command or inherited stdin pipe survived", action)
			}
			if action == "root-exit" {
				if runErr != nil || result["exit_code"] != 0 {
					t.Fatal(result, runErr)
				}
			} else if e, ok := runErr.(*toolerr.Error); !ok || e.Code != "timeout" {
				t.Fatal("expected timeout", result, runErr)
			}
			if status, err := windows.WaitForSingleObject(child, 5000); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("descendant survived", action, status, err)
			}
		})
	}
}

// The helper runs in a separate test process. Its child inherits stdin so the
// runtime cases also exercise cleanup while Cmd.Wait has a blocked stdin copy.
func Test_WindowsExecHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--xmesh-exec-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	switch args[0] {
	case "marker":
		if err := os.WriteFile(args[1], []byte("started"), 0600); err != nil {
			os.Exit(2)
		}
	case "child":
		time.Sleep(5 * time.Minute)
	case "tree":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		child := osexec.Command(exe, helperArgs("child")...)
		child.Stdin = os.Stdin
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(4)
		}
		for {
			if _, err := os.Stat(args[2]); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(5)
	}
	os.Exit(0)
}
