package policy

import (
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Sandbox returns the platform isolation launcher. Unsupported hosts fail closed.
func (e *Engine) Sandbox(cmd string, args []string, cwd string, env []string) (string, []string, []string, error) {
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
	argv = append(argv, "--bind", cwd, cwd, "--chdir", cwd, "--setenv", "HOME", "/tmp", "--setenv", "USERPROFILE", "/tmp", "--", resolved)
	argv = append(argv, args...)
	if !(resolved == cwd || strings.HasPrefix(resolved, cwd+"/") || strings.HasPrefix(resolved, "/usr/") || strings.HasPrefix(resolved, "/bin/") || strings.HasPrefix(resolved, "/sbin/")) {
		return "", nil, nil, toolerr.New("policy_denied", "可执行文件不在 L2 可见路径内", "rule", "sandbox.executable", "reason", "请选择系统或工作目录中的可执行文件")
	}
	return launcher, argv, env, nil
}
