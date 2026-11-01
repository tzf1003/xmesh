package computer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// EngineOptions describes how a Host builds its desktop provider.
type EngineOptions struct {
	Engine Engine
	// NativePath is the Rust executor. With the agent-desktop engine on macOS it
	// is the background coordinate-input fallback and is optional.
	NativePath string
	// AgentDesktopPath is the pinned agent-desktop binary (agent-desktop engine only).
	AgentDesktopPath string
	// StateDir hosts the private scratch directory for screenshots.
	StateDir string
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

// Factories returns the provider constructors for normal and local-authorization starts.
func (o EngineOptions) Factories() (normal, authorized func() (Provider, error), err error) {
	switch o.Engine {
	case "", EngineNative:
		return func() (Provider, error) { return StartProcess(o.NativePath) },
			func() (Provider, error) { return StartAuthorizedProcess(o.NativePath) }, nil
	case EngineAgentDesktop:
	default:
		return nil, nil, fmt.Errorf("未知桌面引擎 %q", o.Engine)
	}
	if !regularFile(o.AgentDesktopPath) {
		return nil, nil, fmt.Errorf("agent-desktop 不是绝对路径下的普通文件: %s", o.AgentDesktopPath)
	}
	tmp := filepath.Join(o.StateDir, "tmp")
	if err = os.MkdirAll(tmp, 0o700); err != nil {
		return nil, nil, err
	}
	// agent-desktop requires TMPDIR to be user-owned and 0700 on macOS.
	if err = os.Chmod(tmp, 0o700); err != nil {
		return nil, nil, err
	}
	run := ExecRunner{Path: o.AgentDesktopPath, TempDir: tmp}
	build := func(start func(string) (*Process, error)) (Provider, error) {
		var fallback Provider
		if runtime.GOOS == "darwin" && regularFile(o.NativePath) {
			// Background coordinate input has no agent-desktop equivalent.
			// If the native module cannot start, foreground still works.
			if p, e := start(o.NativePath); e == nil {
				fallback = p
			}
		}
		return NewAgentDesktop(run, fallback, tmp), nil
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
