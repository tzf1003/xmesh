package adengine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrRestartUnavailable is returned by a Restart hook when no Host service can
// be restarted from here. The switch then stays in place and the caller reports
// that a restart is needed; nothing is rolled back.
var ErrRestartUnavailable = errors.New("桌面 Host 服务不可用，需手动重启")

// Updater moves one device to the pinned release.
type Updater struct {
	Manifest *Manifest
	Key      string
	Store    Store
	Stager   Stager
	Exec     Exec
}

// NewUpdater binds the manifest, the platform key and a state directory.
func NewUpdater(m *Manifest, key, stateDir string, exec Exec) Updater {
	st := NewStore(stateDir)
	return Updater{Manifest: m, Key: key, Store: st, Stager: Stager{Store: st}, Exec: exec}
}

// Change describes what an operation did.
type Change struct {
	From            string `json:"from"`
	To              string `json:"to"`
	Changed         bool   `json:"changed"`
	Pending         bool   `json:"pending_restart,omitempty"`
	RestartRequired bool   `json:"restart_required,omitempty"`
	RolledBack      bool   `json:"rolled_back,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// Hooks connect an update to the running Host. Restart and Verify are optional.
type Hooks struct {
	// Restart restarts the Host so the new engine is loaded.
	Restart func(ctx context.Context) error
	// Verify checks the restarted Host runs the new engine.
	Verify func(ctx context.Context) error
}

func fromName(active string) string {
	if active == "" {
		return "bundled"
	}
	return active
}

func (u Updater) health(ctx context.Context, path string) error {
	return Health(ctx, u.Exec, path)
}

// Install stages the pinned release, checks it with the health commands, and
// makes it active. It restarts nothing. A release that is not validated on this
// platform is refused.
func (u Updater) Install(ctx context.Context) (Change, error) {
	pinned := u.Manifest.Version
	active, err := u.Store.Active()
	if err != nil {
		return Change{}, err
	}
	ch := Change{From: fromName(active), To: pinned}
	if !u.Manifest.Validated(pinned, u.Key) {
		return ch, fmt.Errorf("agent-desktop %s 尚未在 %s 平台验收，拒绝安装", pinned, u.Key)
	}
	if active == pinned {
		if id := IdentifyManaged(u.Store, pinned, u.Key); id.Found && id.Problem == "" {
			return ch, nil
		}
	}
	if _, err := u.Stager.Stage(ctx, u.Manifest, u.Key); err != nil {
		return ch, err
	}
	id := IdentifyManaged(u.Store, pinned, u.Key)
	if id.Problem != "" {
		return ch, fmt.Errorf("安装后校验失败: %s", id.Problem)
	}
	if err := u.health(ctx, id.Path); err != nil {
		return ch, fmt.Errorf("新版本健康检查失败，未切换: %w", err)
	}
	if err := u.Store.Switch(pinned); err != nil {
		return ch, err
	}
	ch.Changed = true
	return ch, nil
}

// Apply is the operator path: install, restart the Host, verify, and roll back to
// the previous engine when the restarted Host fails the check.
func (u Updater) Apply(ctx context.Context, h Hooks) (Change, error) {
	ch, err := u.Install(ctx)
	if err != nil {
		return ch, err
	}
	if !ch.Changed {
		// Already on the pinned release: a deliberate check that passes still resumes
		// automatic updates after a failure streak.
		return ch, u.clearFailures()
	}
	if h.Restart == nil {
		ch.RestartRequired = true
		return ch, nil
	}
	if err := h.Restart(ctx); err != nil {
		if errors.Is(err, ErrRestartUnavailable) {
			ch.RestartRequired = true
			return ch, nil
		}
		return u.rollback(ctx, ch, h, fmt.Errorf("重启 Host 失败: %w", err))
	}
	if h.Verify != nil {
		if err := h.Verify(ctx); err != nil {
			return u.rollback(ctx, ch, h, fmt.Errorf("重启后的 Host 未通过复核: %w", err))
		}
	}
	return ch, u.clearFailures()
}

// clearFailures forgets the automatic-update failure streak. It runs only after a
// deliberate update has been verified.
func (u Updater) clearFailures() error {
	policy, err := u.Store.LoadPolicy()
	if err != nil {
		return nil
	}
	policy.ConsecutiveFailures, policy.LastError = 0, ""
	return u.Store.SavePolicy(policy)
}

// Rollback restores the previous engine and restarts the Host when possible.
func (u Updater) Rollback(ctx context.Context, h Hooks) (Change, error) {
	active, err := u.Store.Active()
	if err != nil {
		return Change{}, err
	}
	previous, err := u.Store.Previous()
	if err != nil {
		return Change{}, err
	}
	ch := Change{From: fromName(active), To: fromName(previous)}
	if err := u.Store.Swap(); err != nil {
		return ch, err
	}
	ch.Changed, ch.RolledBack = true, true
	if h.Restart == nil {
		ch.RestartRequired = true
		return ch, nil
	}
	if err := h.Restart(ctx); err != nil {
		if errors.Is(err, ErrRestartUnavailable) {
			ch.RestartRequired = true
			return ch, nil
		}
		return ch, fmt.Errorf("回滚后重启 Host 失败: %w", err)
	}
	return ch, nil
}

func (u Updater) rollback(ctx context.Context, ch Change, h Hooks, cause error) (Change, error) {
	if err := u.Store.Swap(); err != nil {
		return ch, errors.Join(cause, fmt.Errorf("回滚失败: %w", err))
	}
	ch.RolledBack, ch.Changed = true, false
	if h.Restart != nil {
		if err := h.Restart(ctx); err != nil && !errors.Is(err, ErrRestartUnavailable) {
			cause = errors.Join(cause, fmt.Errorf("回滚后重启 Host 失败: %w", err))
		}
	}
	return ch, cause
}

// CheckPending runs when the Host starts. A switch made by AutoApply must pass
// the health check before the Host serves; otherwise it is rolled back and the
// version is recorded as failed.
func (u Updater) CheckPending(ctx context.Context) (Change, error) {
	pending, err := u.Store.Pending()
	if err != nil || pending == "" {
		return Change{}, err
	}
	active, err := u.Store.Active()
	if err != nil {
		return Change{}, err
	}
	if active != pending {
		return Change{Reason: "superseded"}, u.Store.SetPending("")
	}
	ch := Change{To: pending}
	cause := u.checkActive(ctx, pending)
	policy, err := u.Store.LoadPolicy()
	if err != nil {
		return ch, err
	}
	if cause == nil {
		if err := u.Store.SetPending(""); err != nil {
			return ch, err
		}
		policy.ConsecutiveFailures, policy.LastError = 0, ""
		ch.Reason = "committed"
		return ch, u.Store.SavePolicy(policy)
	}
	if err := u.Store.Swap(); err != nil {
		return ch, errors.Join(cause, fmt.Errorf("回滚失败: %w", err))
	}
	if err := u.Store.SetPending(""); err != nil {
		return ch, err
	}
	policy.ConsecutiveFailures++
	policy.LastError = cause.Error()
	policy.FailedVersions = appendUnique(policy.FailedVersions, pending)
	if err := u.Store.SavePolicy(policy); err != nil {
		return ch, err
	}
	ch.Changed, ch.RolledBack, ch.Reason = true, true, cause.Error()
	return ch, nil
}

func (u Updater) checkActive(ctx context.Context, version string) error {
	id := IdentifyManaged(u.Store, version, u.Key)
	if id.Problem != "" {
		return fmt.Errorf("待生效版本校验失败: %s", id.Problem)
	}
	return u.health(ctx, id.Path)
}

// AutoApply is the unattended path. It stages and checks the pinned release only
// when every gate passes, then marks the switch pending so the Host verifies it
// at its next start. The second result is the reason nothing was done, or "".
//
// Gates: policy must be apply; the state must be update_available, meaning a
// newer validated release exists; the platform must not be macOS, where an
// engine change can invalidate the Accessibility and Screen Recording grants,
// so it requires a person; the release must be on the same major.minor line;
// and automatic updates must not be suspended.
func (u Updater) AutoApply(ctx context.Context, mode string, st Status) (Change, string, error) {
	if mode != ModeApply {
		return Change{}, "mode_" + mode, nil
	}
	if st.State != StateUpdateAvailable {
		return Change{}, "state_" + st.State, nil
	}
	if strings.HasPrefix(u.Key, "darwin-") {
		return Change{}, "macos_requires_confirmation", nil
	}
	pinned := u.Manifest.Version
	if !samePatchLine(st.Installed, pinned) {
		return Change{}, "not_patch_release", nil
	}
	policy, err := u.Store.LoadPolicy()
	if err != nil {
		return Change{}, "", err
	}
	if policy.Suspended || policy.ConsecutiveFailures >= suspendAfter {
		return Change{}, "suspended", nil
	}
	if slices.Contains(policy.FailedVersions, pinned) {
		return Change{}, "previously_failed", nil
	}
	ch, err := u.Install(ctx)
	if err != nil {
		policy.ConsecutiveFailures++
		policy.LastError = err.Error()
		if serr := u.Store.SavePolicy(policy); serr != nil {
			err = errors.Join(err, serr)
		}
		return ch, "", err
	}
	if !ch.Changed {
		return ch, "already_current", nil
	}
	if err := u.Store.SetPending(pinned); err != nil {
		return ch, "", err
	}
	ch.Pending = true
	return ch, "", nil
}

// samePatchLine reports whether two X.Y.Z versions share major and minor.
func samePatchLine(a, b string) bool {
	va, errA := ParseVersion(a)
	vb, errB := ParseVersion(b)
	return errA == nil && errB == nil && va.Major == vb.Major && va.Minor == vb.Minor
}
