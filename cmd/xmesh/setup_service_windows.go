package main

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/onboarding"
	"golang.org/x/sys/windows"
)

func waitSetupServiceStopped(ctx context.Context, p *onboarding.Plan) error {
	ep, err := control.ReadEndpoint(filepath.Dir(p.ConfigPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	limit := kernelExitLimit
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(deadline))
	}
	if limit <= 0 {
		return context.DeadlineExceeded
	}
	return waitKernelExit(ep, limit)
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
		var status any
		snap.Running = control.NewClient(ep).Fetch(ctx, "/v1/status", &status) == nil
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
	_, _ = taskCommand(dir, "/End", "/TN", snap.Path)
	// Task Scheduler acknowledges /End before the old kernel releases ports.
	// Wait for that exact process before restoring and starting its replacement.
	if e = waitSetupServiceStopped(ctx, p); e != nil {
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
				var status any
				if control.NewClient(ep).Fetch(ctx, "/v1/status", &status) == nil {
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
