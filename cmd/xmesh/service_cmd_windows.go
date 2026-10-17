package main

import (
	"errors"
	"fmt"
	"os/exec"
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
	if _, err := taskCommand(directory, "/Query", "/TN", name); err != nil {
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
		return run("/Run", "/TN", name)
	case "stop":
		return run("/End", "/TN", name)
	case "restart":
		// /End fails when the task is not running; restart then just starts it.
		old, known := runningKernel(c.Name)
		_ = run("/End", "/TN", name)
		// /End returns before the old kernel has exited. Starting the task at once makes a second
		// kernel that cannot bind the gateway and agent ports yet: it exits with 1 and nothing
		// comes back. So the new kernel starts only after the old process is gone.
		if known {
			if err := waitKernelExit(old, kernelExitLimit); err != nil {
				return err
			}
		}
		if err := run("/Run", "/TN", name); err != nil {
			return err
		}
		if known {
			return waitKernelStart(c.Name, old.PID, kernelStartLimit)
		}
		return nil
	}
	return fmt.Errorf("未知服务操作: %s", action)
}

// runningKernel is the control endpoint of the kernel that serves the service, read from the
// default state directory. It reports false when there is none, for instance because the kernel
// keeps its state in a --state-dir the restart does not know about.
func runningKernel(serviceID string) (control.Endpoint, bool) {
	dir, err := app.StateDir(strings.TrimPrefix(serviceID, "xmesh-"))
	if err != nil {
		return control.Endpoint{}, false
	}
	e, err := control.ReadEndpoint(dir)
	return e, err == nil
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

// waitKernelStart checks that the task started a kernel: it publishes a control.json with another
// PID. Without this a kernel that exits during startup would still make restart report success.
func waitKernelStart(serviceID string, oldPID int, limit time.Duration) error {
	for deadline := time.Now().Add(limit); ; {
		if e, ok := runningKernel(serviceID); ok && e.PID != oldPID {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 内没有看到新内核发布 control.json（进程已变化才算启动），请用 xmesh status 确认或查看节点日志", limit)
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
