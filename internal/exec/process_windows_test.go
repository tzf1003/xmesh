//go:build windows

package exec

import (
	"context"
	"encoding/json"
	"io"
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

func Test_WindowsExecLaunchContract(t *testing.T) {
	exe := windowsExecRuntime(t)
	root := filepath.Join(t.TempDir(), "工作 目录")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	// An explicit relative executable without a suffix must resolve under cwd.
	source, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.Create(filepath.Join(root, "helper.exe"))
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal(copyErr, closeErr)
	}
	t.Setenv("XMESH_PARENT_PRIVATE", "parent-only")
	r, err := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(config.Exec{MaxConcurrent: 1, ExclusiveSlots: 1, DefaultTimeout: "15s", IdempotencyCache: 10, InlineOutputBytes: "64KiB"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	arguments := []string{"", "空 格", `a"b`, `a\\`, `end space \\`}
	input := "输入\x00data\n" + strings.Repeat("abc", 4096)
	raw, _ := json.Marshal(Object{"cmd": `.\helper`, "args": helperArgs("proof", arguments...), "stdin": input, "env": map[string]string{"PROOF_VALUE": "子环境"}})
	result, err := m.Run(context.Background(), "test", r, p, raw)
	if err != nil || result["exit_code"] != 37 || result["stderr"] != "proof-stderr" {
		if failure, ok := err.(*toolerr.Error); ok {
			t.Fatal(result, failure.Object())
		}
		t.Fatal(result, err)
	}
	var proof struct {
		Arguments                              []string
		Directory, Input, Environment, Private string
	}
	if err := json.Unmarshal([]byte(result["stdout"].(string)), &proof); err != nil {
		t.Fatal(err, result)
	}
	want, _ := json.Marshal(arguments)
	got, _ := json.Marshal(proof.Arguments)
	if string(want) != string(got) || !strings.EqualFold(proof.Directory, root) || proof.Input != input || proof.Environment != "子环境" || proof.Private != "" {
		t.Fatal("launch contract mismatch", proof)
	}
}

func Test_WindowsExecCommandPath(t *testing.T) {
	for _, test := range []struct{ path, directory, want string }{
		{`helper.exe`, `C:\work`, `C:\work\helper.exe`},
		{`.\helper.exe`, `C:\work`, `C:\work\helper.exe`},
		{`\bin\helper.exe`, `C:\work`, `C:\bin\helper.exe`},
		{`c:helper.exe`, `C:\work`, `C:\work\helper.exe`},
		{`D:\bin\helper.exe`, `C:\work`, `D:\bin\helper.exe`},
		{`\\server\share\helper.exe`, `C:\work`, `\\server\share\helper.exe`},
	} {
		got, err := commandPath(test.path, test.directory)
		if err != nil || !strings.EqualFold(got, test.want) {
			t.Fatal(test, got, err)
		}
	}
	if _, err := commandPath("", `C:\work`); err == nil {
		t.Fatal("accepted empty executable")
	}
	if _, err := commandPath("C:", `C:\work`); err == nil {
		t.Fatal("accepted drive without executable")
	}
}

func Test_WindowsExecAtomicJobHostCrash(t *testing.T) {
	exe := windowsExecRuntime(t)
	pidPath := filepath.Join(t.TempDir(), "command.pid")
	host := osexec.Command(exe, helperArgs("host", pidPath)...)
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = host.Process.Kill()
		if !waited {
			_ = host.Wait()
		}
	})
	var child windows.Handle
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if data, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.ParseUint(string(data), 10, 32); err == nil {
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
		t.Fatal("host did not publish command")
	}
	t.Cleanup(func() { _ = windows.TerminateProcess(child, 1); _ = windows.CloseHandle(child) })
	if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("command exited before host crash", status, err)
	}
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	waited = true
	if status, err := windows.WaitForSingleObject(child, 5000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("command survived host crash", status, err)
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

				output, err := os.CreateTemp(t.TempDir(), "powershell-*.txt")
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				cmd.Stdout, cmd.Stderr = output, output
				var wait func() error
				if tracked {
					var finish func()
					wait, finish, err = startCommand(cmd)
					if err != nil {
						t.Fatal(err)
					}
					defer finish()
				} else {
					if err := cmd.Start(); err != nil {
						t.Fatal(err)
					}
					wait = cmd.Wait
				}
				done := make(chan error, 1)
				go func() { done <- wait() }()
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Fatal("PowerShell launch did not complete")
				}
				data, readErr := os.ReadFile(output.Name())
				if err != nil || readErr != nil || strings.TrimSpace(string(data)) != "hello" {
					t.Fatal("PowerShell output", string(data), err, readErr)
				}
			})
		}
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
	case "proof":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(20)
		}
		directory, err := os.Getwd()
		if err != nil {
			os.Exit(21)
		}
		proof := struct {
			Arguments                              []string
			Directory, Input, Environment, Private string
		}{args[1:], directory, string(input), os.Getenv("PROOF_VALUE"), os.Getenv("XMESH_PARENT_PRIVATE")}
		if err := json.NewEncoder(os.Stdout).Encode(proof); err != nil {
			os.Exit(22)
		}
		_, _ = os.Stderr.WriteString("proof-stderr")
		os.Exit(37)
	case "host":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(10)
		}
		output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			os.Exit(11)
		}
		command := osexec.Command(exe, helperArgs("child")...)
		command.Stdout, command.Stderr = output, output
		if _, _, err := startCommand(command); err != nil {
			os.Exit(12)
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(command.Process.Pid)), 0600); err != nil {
			os.Exit(13)
		}
		time.Sleep(5 * time.Minute)
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
