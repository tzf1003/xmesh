package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/control"
	"golang.org/x/sys/windows"
)

// errTaskMissing means the logon task of this node is not registered for the current user.
var errTaskMissing = fmt.Errorf("%w: 登录任务不存在", service.ErrNotInstalled)

// Limits for a task restart. A hard stop ends the kernel at once, so the old process normally
// exits within milliseconds; the limits only bound a process that will not die or a new kernel
// that never publishes its control.json.
const (
	kernelExitLimit  = 30 * time.Second
	kernelStartLimit = 20 * time.Second
)

// platformServiceMissing recognises the SCM's "no such service".
func platformServiceMissing(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
}

// userTaskAction drives the logon task that install registers for the current user.
func userTaskAction(action string, c *service.Config) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	name := userTaskName(c.Name, user.User.Sid.String())
	// A task that was never registered is reported by schtasks in the system language, so ask
	// it directly: /Query exits 1 when there is no such task.
	document, err := taskCommand(directory, "/Query", "/TN", name, "/XML")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return errTaskMissing
		}
		return err
	}
	run := func(args ...string) error { _, err := taskCommand(directory, args...); return err }
	switch action {
	case "uninstall":
		return run("/Delete", "/TN", name, "/F")
	case "start":
		// Computer hosts have their own readiness protocol in computerService.
		if c.Name == computerServiceName {
			return run("/Run", "/TN", name)
		}
		dir, err := taskStateDir(taskDocumentText(document), c.Name)
		if err != nil {
			return err
		}
		if err := run("/Run", "/TN", name); err != nil {
			return err
		}
		// /Run only acknowledges scheduling. A kernel can exit immediately (for
		// example with an unmigrated config), so require a responding control
		// channel. An already running task may keep its existing endpoint.
		return waitKernelStart(dir, control.Endpoint{}, time.Time{}, kernelStartLimit)
	case "stop":
		return run("/End", "/TN", name)
	case "restart":
		// /End fails when the task is not running; restart then just starts it.
		dir, err := taskStateDir(taskDocumentText(document), c.Name)
		if err != nil {
			return err
		}
		old, err := control.ReadEndpoint(dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		known := err == nil
		_ = run("/End", "/TN", name)
		// /End returns before the old kernel has exited. Starting the task at once makes a second
		// kernel that cannot bind the gateway and agent ports yet: it exits with 1 and nothing
		// comes back. So the new kernel starts only after the old process is gone.
		if known {
			if err := waitKernelExit(old, kernelExitLimit); err != nil {
				return err
			}
		}
		started := time.Now()
		if err := run("/Run", "/TN", name); err != nil {
			return err
		}
		return waitKernelStart(dir, old, started, kernelStartLimit)
	}
	return fmt.Errorf("未知服务操作: %s", action)
}

// taskStateDir resolves the state directory from the task's actual command line and working
// directory. Installed tasks created before --state-dir was set retain the per-node default.
func taskStateDir(document, serviceID string) (string, error) {
	task, err := parseUserTask(document)
	if err != nil {
		return "", err
	}
	args, err := windows.DecomposeCommandLine("xmesh.exe " + task.Actions.Exec.Arguments)
	if err != nil {
		return "", err
	}
	state := ""
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--state-dir" || arg == "-state-dir" {
			i++
			if i == len(args) {
				return "", fmt.Errorf("登录任务的 --state-dir 需要目录")
			}
			state = args[i]
		} else if strings.HasPrefix(arg, "--state-dir=") || strings.HasPrefix(arg, "-state-dir=") {
			state = strings.SplitN(arg, "=", 2)[1]
		}
	}
	if state == "" {
		return app.StateDir(strings.TrimPrefix(serviceID, "xmesh-"))
	}
	if !filepath.IsAbs(state) {
		if task.Actions.Exec.WorkingDirectory == "" {
			return "", fmt.Errorf("登录任务的相对状态目录需要 WorkingDirectory")
		}
		state = filepath.Join(task.Actions.Exec.WorkingDirectory, state)
	}
	return state, nil
}

// waitKernelExit returns once the process that wrote e has exited. control.json outlives a
// kernel that was killed or crashed, so a PID that now belongs to a process created after the
// kernel started is not that kernel any more, and counts as exited.
func waitKernelExit(e control.Endpoint, limit time.Duration) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(e.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return err
	}
	// The kernel publishes StartedAt after its process was created; the margin covers the rounding
	// of StartedAt to milliseconds and the gap between process creation and the first write.
	if time.Unix(0, created.Nanoseconds()).After(e.StartedAt.Add(2 * time.Second)) {
		return nil
	}
	event, err := windows.WaitForSingleObject(h, uint32(limit/time.Millisecond))
	if err != nil {
		return err
	}
	if event != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("旧内核（进程 %d）在 %s 内没有退出，未重新启动", e.PID, limit)
	}
	return nil
}

// waitKernelStart requires a fresh endpoint and a successful control request before accepting
// the restarted task, including when the previous kernel had already stopped.
func waitKernelStart(dir string, old control.Endpoint, started time.Time, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		if e, err := control.ReadEndpoint(dir); err == nil && e.StartedAt.After(old.StartedAt.Time) && !e.StartedAt.Before(started.Truncate(time.Millisecond)) {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			r, err := control.NewClient(e).Do(ctx, "GET", "/v2/status", nil, nil)
			cancel()
			if err == nil && r.Error() == nil {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("内核在 %s 内启动验收超时，请用 xmesh status 确认或查看节点日志", limit)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// installerSID is the SID of the user running install, or "" if unavailable.
func installerSID() string {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}
