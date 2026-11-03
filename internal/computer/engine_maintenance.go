package computer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tzf1003/xmesh/internal/computer/adengine"
)

// engineExec runs agent-desktop executables with the Host's private temp directory,
// as the adapter does.
func engineExec(stateDir string) adengine.Exec {
	tmp := filepath.Join(stateDir, "tmp")
	return func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return nil, err
		}
		return ExecRunner{Path: path, TempDir: tmp}.Run(ctx, args...)
	}
}

// NewEngineUpdater binds the embedded manifest to this device's platform.
func NewEngineUpdater(stateDir string) (adengine.Updater, error) {
	m, err := adengine.Embedded()
	if err != nil {
		return adengine.Updater{}, err
	}
	return adengine.NewUpdater(m, adengine.PlatformKey(runtime.GOOS, runtime.GOARCH), stateDir, engineExec(stateDir)), nil
}

// CheckPendingEngine runs before the Host serves. An engine that was switched by
// the automatic path is health-checked here and rolled back if it fails.
func CheckPendingEngine(ctx context.Context, stateDir string) (adengine.Change, error) {
	u, err := NewEngineUpdater(stateDir)
	if err != nil {
		return adengine.Change{}, err
	}
	return u.CheckPending(ctx)
}

// EngineReport is the device-side engine state printed by `xmesh computer engine status`.
type EngineReport struct {
	Status  adengine.Status `json:"status"`
	Pending string          `json:"pending,omitempty"`
	Policy  adengine.Policy `json:"auto_update_state"`
}

// ReportEngine resolves the engine the Host would run and the update bookkeeping
// around it. It reads files only; it does not start the desktop executor.
func ReportEngine(stateDir, bundledPath string) (EngineReport, error) {
	conf, err := ReadEngine(stateDir)
	if err != nil {
		return EngineReport{}, err
	}
	res, err := EngineOptions{Engine: conf, StateDir: stateDir, BundledPath: bundledPath}.Resolve()
	if err != nil {
		return EngineReport{}, err
	}
	store := adengine.NewStore(stateDir)
	pending, err := store.Pending()
	if err != nil {
		return EngineReport{}, err
	}
	policy, err := store.LoadPolicy()
	if err != nil {
		return EngineReport{}, err
	}
	return EngineReport{Status: res.Status, Pending: pending, Policy: policy}, nil
}

// EngineAutoUpdate reports the configured auto-update policy.
func (m *Manager) EngineAutoUpdate() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Effective().Engine.AutoUpdate
}

// RunEngineMaintenance applies the auto-update policy every interval until ctx
// ends. The first pass waits two minutes so that startup stays quick. In check
// mode it does nothing: the state is visible through Info and engine status.
func RunEngineMaintenance(ctx context.Context, stateDir string, mode func() string, every time.Duration) {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := maintainEngine(ctx, stateDir, mode()); err != nil {
			slog.Warn("agent_desktop_auto_update", "error", err)
		}
		timer.Reset(every)
	}
}

func maintainEngine(ctx context.Context, stateDir, mode string) error {
	if mode != adengine.ModeApply {
		return nil
	}
	conf, err := ReadEngine(stateDir)
	if err != nil || conf != EngineAgentDesktop {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	res, err := EngineOptions{Engine: conf, StateDir: stateDir, BundledPath: AgentDesktopHelperPath(exe)}.Resolve()
	if err != nil {
		return err
	}
	u, err := NewEngineUpdater(stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	ch, reason, err := u.AutoApply(ctx, mode, res.Status)
	if err != nil {
		return err
	}
	if ch.Pending {
		slog.Info("agent_desktop_update_staged", "from", ch.From, "to", ch.To, "effective", "next_host_start")
	}
	if reason != "" {
		slog.Debug("agent_desktop_auto_update_skipped", "reason", reason)
	}
	return nil
}
