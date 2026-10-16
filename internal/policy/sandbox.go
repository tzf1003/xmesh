package policy

import (
	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// UseSandboxRunner installs a managed isolation runner for L2 execution. It
// must be called before the engine is shared. No runner is installed by default:
// platforms without a native launcher then fail closed in Sandbox and SandboxPTY.
func (e *Engine) UseSandboxRunner(r sandbox.Runner) { e.runner = r }

// SandboxRunner returns the managed runner that must execute commands when L2 is
// configured, or nil when the launcher form from Sandbox/SandboxPTY applies.
func (e *Engine) SandboxRunner() sandbox.Runner {
	if e.config.Isolation != "L2" {
		return nil
	}
	return e.runner
}

// Sandbox returns the platform isolation launcher. Unsupported hosts fail closed.
func (e *Engine) Sandbox(cmd string, args []string, cwd string, env []string) (string, []string, []string, error) {
	return e.sandbox(cmd, args, cwd, env, false)
}

// SandboxPTY reports whether the launcher must acquire its controlling terminal
// inside the sandbox. The PTY allocator must leave that terminal unclaimed.
func (e *Engine) SandboxPTY(cmd string, args []string, cwd string, env []string) (string, []string, []string, bool, error) {
	innerTerminal := e.config.Isolation == "L2" && runtime.GOOS == "linux"
	launch, argv, env, err := e.sandbox(cmd, args, cwd, env, true)
	return launch, argv, env, innerTerminal, err
}

func (e *Engine) sandbox(cmd string, args []string, cwd string, env []string, terminal bool) (string, []string, []string, error) {
	if e.config.Isolation != "L2" {
		return cmd, args, env, nil
	}
	if runtime.GOOS == "darwin" {
		return seatbelt(cmd, args, cwd, env)
	}
	if runtime.GOOS != "linux" {
		return "", nil, nil, toolerr.New("policy_denied", "此平台尚未提供 L2 执行隔离", "rule", "sandbox.unavailable", "reason", "请使用支持 bubblewrap 的 Linux 节点")
	}
	launcher, err := exec.LookPath("bwrap")
	if err != nil {
		return "", nil, nil, toolerr.New("policy_denied", "L2 需要 bubblewrap", "rule", "sandbox.unavailable", "reason", "未找到 bwrap")
	}
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		return "", nil, nil, toolerr.Args("cmd")
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", nil, nil, err
	}
	argv := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"}
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if st, err := os.Lstat(dir); err == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(dir)
				if err != nil {
					return "", nil, nil, err
				}
				argv = append(argv, "--symlink", target, dir)
			} else {
				argv = append(argv, "--ro-bind", dir, dir)
			}
		}
	}
	// Expose only this workspace. Home credential directories remain outside it.
	if err := credentialWorkspace(cwd); err != nil {
		return "", nil, nil, err
	}
	argv = append(argv, "--bind", cwd, cwd, "--chdir", cwd, "--setenv", "HOME", "/tmp", "--setenv", "USERPROFILE", "/tmp", "--")
	if terminal {
		// Acquire the slave after entering the PID namespace, so the shell's
		// session and foreground process groups have namespace-local identities.
		setsid, err := exec.LookPath("setsid")
		if err == nil {
			setsid, err = filepath.EvalSymlinks(setsid)
		}
		if err != nil || !(strings.HasPrefix(setsid, "/usr/") || strings.HasPrefix(setsid, "/bin/")) {
			return "", nil, nil, toolerr.New("policy_denied", "Linux L2 终端需要 util-linux setsid", "rule", "sandbox.unavailable", "reason", "未找到系统 setsid")
		}
		argv = append(argv, setsid, "--ctty", "--")
	}
	argv = append(argv, resolved)
	argv = append(argv, args...)
	if !(resolved == cwd || strings.HasPrefix(resolved, cwd+"/") || strings.HasPrefix(resolved, "/usr/") || strings.HasPrefix(resolved, "/bin/") || strings.HasPrefix(resolved, "/sbin/")) {
		return "", nil, nil, toolerr.New("policy_denied", "可执行文件不在 L2 可见路径内", "rule", "sandbox.executable", "reason", "请选择系统或工作目录中的可执行文件")
	}
	return launcher, argv, env, nil
}
