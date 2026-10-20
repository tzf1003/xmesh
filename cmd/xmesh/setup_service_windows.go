package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/onboarding"
	"golang.org/x/sys/windows"
)

func prepareSetupServiceStop(ctx context.Context, p *onboarding.Plan) (func() error, error) {
	ep, err := control.ReadEndpoint(filepath.Dir(p.ConfigPath))
	if err != nil {
		if os.IsNotExist(err) {
			// A stopped task can be replaced without a control endpoint. A
			// running task without its process identity needs explicit repair.
			if err := verifyStoppedSetupTask(ctx, p); err != nil {
				return nil, err
			}
			return func() error { return nil }, nil
		}
		return nil, err
	}
	return func() error {
		limit := kernelExitLimit
		if deadline, ok := ctx.Deadline(); ok {
			limit = min(limit, time.Until(deadline))
		}
		if limit <= 0 {
			return context.DeadlineExceeded
		}
		return waitKernelExit(ep, limit)
	}, nil
}

func verifyStoppedSetupTask(ctx context.Context, p *onboarding.Plan) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	name := userTaskName(serviceName(p.Input.NodeName), user.User.Sid.String())
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	// Task Scheduler's COM State is numeric and independent of system language.
	script := `$ErrorActionPreference='Stop'; $s=New-Object -ComObject Schedule.Service; $s.Connect(); $t=$s.GetFolder('\').GetTask('` + strings.ReplaceAll(name, "'", "''") + `'); if ($t.State -eq 4) { exit 2 }`
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "WindowsPowerShell", "v2.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-Command", script)
	if err := cmd.Run(); err != nil {
		return errors.New("无法确认旧服务进程已停止，请检查任务管理器和本机服务状态")
	}
	return nil
}

func snapshotSetupService(ctx context.Context, p *onboarding.Plan) (onboarding.ServiceSnapshot, error) {
	var snap onboarding.ServiceSnapshot
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return snap, e
	}
	dir, e := windows.GetSystemDirectory()
	if e != nil {
		return snap, e
	}
	snap.Path = userTaskName(serviceName(p.Input.NodeName), user.User.Sid.String())
	raw, e := taskCommand(dir, "/Query", "/TN", snap.Path, "/XML")
	if e != nil {
		return snap, nil
	}
	snap.Installed = true
	snap.Definition = raw
	var task userTask
	d := xml.NewDecoder(strings.NewReader(taskDocumentText(raw)))
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if e = d.Decode(&task); e != nil {
		return snap, e
	}
	args, e := windows.DecomposeCommandLine(task.Actions.Exec.Arguments)
	if e != nil || len(args) < 3 || args[0] != "serve" {
		return snap, errors.New("invalid registered service")
	}
	match := false
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--config" && strings.EqualFold(filepath.Clean(args[i+1]), filepath.Clean(p.ConfigPath)) {
			match = true
		}
	}
	if !match {
		return snap, &onboarding.Error{Code: "service_conflict", Message: "同名服务使用另一个配置，请选择新的设备名", Field: "node_name"}
	}
	// A live owner-only control channel is the authoritative running signal.
	if ep, e := control.ReadEndpoint(filepath.Dir(p.ConfigPath)); e == nil {
		snap.Running = setupServiceResponding(ctx, ep) == nil
	}
	return snap, nil
}

func restoreSetupService(ctx context.Context, p *onboarding.Plan, snap onboarding.ServiceSnapshot) error {
	if !snap.Installed {
		return nil
	}
	dir, e := windows.GetSystemDirectory()
	if e != nil {
		return e
	}
	wait, e := prepareSetupServiceStop(ctx, p)
	if e != nil {
		return e
	}
	_, _ = taskCommand(dir, "/End", "/TN", snap.Path)
	// Task Scheduler acknowledges /End before the old kernel releases ports.
	// Wait for that exact process before restoring and starting its replacement.
	if e = wait(); e != nil {
		return e
	}
	tmp, e := os.CreateTemp("", "xmesh-service-restore-*.xml")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(snap.Definition); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	if _, e = taskCommand(dir, "/Create", "/TN", snap.Path, "/XML", name, "/F"); e != nil {
		return e
	}
	if snap.Running {
		_, e = taskCommand(dir, "/Run", "/TN", snap.Path)
		if e != nil {
			return e
		}
		for {
			// The restored config may predate onboarding and its PAT grant.
			// Use the same owner-only status channel as the saved baseline.
			if ep, err := control.ReadEndpoint(filepath.Dir(p.ConfigPath)); err == nil {
				if setupServiceResponding(ctx, ep) == nil {
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return e
}

// A rollback may restore a pre-upgrade kernel. Read only its advertised status
// endpoint; configuration operations continue to require the current API major.
func setupServiceResponding(ctx context.Context, ep control.Endpoint) error {
	major := strings.Split(ep.APIVersion, ".")[0]
	if major != "1" && major != "2" {
		return errors.New("unsupported saved control API")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := control.NewClient(ep).Stream(ctx, "/v"+major+"/status", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || strings.Split(res.Header.Get("X-Xmesh-Control-Version"), ".")[0] != major {
		return errors.New("saved service status unavailable")
	}
	var status struct {
		Node struct {
			Name string `json:"name"`
		} `json:"node"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&status); err != nil {
		return err
	}
	if status.Node.Name != ep.Node {
		return fmt.Errorf("saved service identity differs")
	}
	return nil
}
