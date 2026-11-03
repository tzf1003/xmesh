package computer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tzf1003/xmesh/internal/computer/adengine"
)

// EngineOptions describes how a Host builds its desktop provider.
type EngineOptions struct {
	Engine Engine
	// NativePath is the Rust executor. With the agent-desktop engine on macOS it
	// is the background coordinate-input fallback and is optional.
	NativePath string
	// AgentDesktopPath is an explicit agent-desktop executable (xmesh --agent-desktop).
	// It is verified against the manifest exactly like the bundled copy.
	AgentDesktopPath string
	// BundledPath is the copy shipped beside xmesh; see AgentDesktopHelperPath.
	BundledPath string
	// StateDir holds the managed installs and the private scratch directory.
	StateDir string
}

// EngineResolution is the engine a Host runs after verification.
type EngineResolution struct {
	Configured Engine
	Effective  Engine
	Status     adengine.Status
	// Path is the agent-desktop executable that was verified, when there is one.
	Path string
}

// Resolve verifies the agent-desktop executable and decides which engine runs.
// An executable that fails verification, or a release the manifest does not
// accept on this platform, falls back to native; the reason is in Status.
func (o EngineOptions) Resolve() (EngineResolution, error) {
	conf := EngineNative
	if o.Engine != "" {
		parsed, err := ParseEngine(string(o.Engine))
		if err != nil {
			return EngineResolution{}, err
		}
		conf = parsed
	}
	m, err := adengine.Embedded()
	if err != nil {
		return EngineResolution{}, err
	}
	key := adengine.PlatformKey(runtime.GOOS, runtime.GOARCH)
	id := o.identify(m, key)
	st := adengine.Evaluate(m, key, string(conf), id)
	return EngineResolution{Configured: conf, Effective: Engine(st.Effective), Status: st, Path: id.Path}, nil
}

// identify picks the executable to verify: an explicit path, then the managed
// install named by the active pointer, then the bundled copy.
func (o EngineOptions) identify(m *adengine.Manifest, key string) adengine.Identity {
	if o.AgentDesktopPath != "" {
		return adengine.IdentifyBundled(m, key, o.AgentDesktopPath)
	}
	if o.StateDir != "" {
		store := adengine.NewStore(o.StateDir)
		active, err := store.Active()
		if err != nil {
			return adengine.Identity{Source: adengine.SourceManaged, Problem: adengine.ProblemPointer}
		}
		if active != "" {
			return adengine.IdentifyManaged(store, active, key)
		}
	}
	if o.BundledPath == "" {
		return adengine.Identity{Source: adengine.SourceBundled, Problem: adengine.ProblemNotInstalled}
	}
	return adengine.IdentifyBundled(m, key, o.BundledPath)
}

// AgentDesktopHelperPath locates the pinned binary shipped beside the xmesh
// executable. On macOS it lives inside the signed computer app so the
// accessibility grant is recorded under the xmesh identity.
func AgentDesktopHelperPath(executable string) string {
	name := "agent-desktop"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if runtime.GOOS == "darwin" {
		app := filepath.Join(filepath.Dir(executable), "xmesh-computer.app", "Contents", "MacOS", name)
		if st, err := os.Stat(app); err == nil && st.Mode().IsRegular() {
			return app
		}
	}
	return filepath.Join(filepath.Dir(executable), name)
}

func regularFile(path string) bool {
	st, err := os.Stat(path)
	return filepath.IsAbs(path) && err == nil && st.Mode().IsRegular()
}

// Factories returns the provider constructors for normal and local-authorization
// starts. Every provider reports the resolved engine through Info.
func (o EngineOptions) Factories() (normal, authorized func() (Provider, error), err error) {
	res, err := o.Resolve()
	if err != nil {
		return nil, nil, err
	}
	info := engineInfo{engine: res.Effective, status: res.Status.State, reason: res.Status.Reason}
	if res.Effective == EngineAgentDesktop {
		// The version is reported only for the engine that actually runs.
		info.version = res.Status.Installed
	}
	if res.Effective != EngineAgentDesktop {
		normal = func() (Provider, error) {
			p, err := StartProcess(o.NativePath)
			if err != nil {
				return nil, err
			}
			return withEngine(p, info), nil
		}
		authorized = func() (Provider, error) {
			p, err := StartAuthorizedProcess(o.NativePath)
			if err != nil {
				return nil, err
			}
			return withEngine(p, info), nil
		}
		return normal, authorized, nil
	}
	tmp := filepath.Join(o.StateDir, "tmp")
	if err = os.MkdirAll(tmp, 0o700); err != nil {
		return nil, nil, err
	}
	// agent-desktop requires TMPDIR to be user-owned and 0700 on macOS.
	if err = os.Chmod(tmp, 0o700); err != nil {
		return nil, nil, err
	}
	run := ExecRunner{Path: res.Path, TempDir: tmp}
	build := func(start func(string) (*Process, error)) (Provider, error) {
		var fallback Provider
		if runtime.GOOS == "darwin" && regularFile(o.NativePath) {
			// Background coordinate input has no agent-desktop equivalent.
			// If the native module cannot start, foreground still works.
			if p, e := start(o.NativePath); e == nil {
				fallback = p
			}
		}
		return withEngine(NewAgentDesktop(run, fallback, tmp), info), nil
	}
	normal = func() (Provider, error) { return build(StartProcess) }
	authorized = func() (Provider, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Only this path may ask the OS to prompt; ignore the verdict, info reports it.
		_, _ = run.Run(ctx, "permissions", "--request")
		return build(StartAuthorizedProcess)
	}
	return normal, authorized, nil
}

// engineInfo is the resolved engine as reported to clients.
type engineInfo struct {
	engine  Engine
	version string
	status  string
	reason  string
}

// engineProvider stamps the resolved engine onto every Info it reports.
type engineProvider struct {
	Provider
	engine engineInfo
}

func withEngine(p Provider, e engineInfo) Provider { return engineProvider{Provider: p, engine: e} }

// Info adds the engine fields to the executor's identity.
func (p engineProvider) Info(ctx context.Context) (Info, error) {
	info, err := p.Provider.Info(ctx)
	info.Engine = string(p.engine.engine)
	info.EngineVersion = p.engine.version
	info.EngineStatus = p.engine.status
	info.EngineReason = p.engine.reason
	return info, err
}

// String keeps error text readable in logs.
func (e EngineResolution) String() string {
	return fmt.Sprintf("engine=%s configured=%s state=%s reason=%s", e.Effective, e.Configured, e.Status.State, e.Status.Reason)
}
