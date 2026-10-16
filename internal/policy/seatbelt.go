package policy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// seatbelt uses parameterized paths so workspace names are never profile code.
func seatbelt(cmd string, args []string, cwd string, env []string) (string, []string, []string, error) {
	const launcher = "/usr/bin/sandbox-exec"
	if info, err := os.Stat(launcher); err != nil || !info.Mode().IsRegular() {
		return "", nil, nil, toolerr.New("policy_denied", "L2 需要系统 sandbox-exec", "rule", "sandbox.unavailable", "reason", "系统未提供沙箱启动器")
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil || !filepath.IsAbs(root) {
		return "", nil, nil, toolerr.Args("cwd")
	}
	if err := credentialWorkspace(root); err != nil {
		return "", nil, nil, err
	}
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		return "", nil, nil, toolerr.Args("cmd")
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", nil, nil, err
	}
	visible := within(resolved, root)
	for _, system := range []string{"/bin", "/sbin", "/usr", "/System", "/opt/homebrew"} {
		visible = visible || within(resolved, system)
	}
	if !visible {
		return "", nil, nil, toolerr.New("policy_denied", "可执行文件不在 L2 可见路径内", "rule", "sandbox.executable", "reason", "请选择系统或工作目录中的可执行文件")
	}
	// Reading the root directory is required by dyld's Cryptex discovery.
	profile := `(version 1)
(deny default)
(deny sysctl-read (sysctl-name-regex #"^kern[.]proc"))
(deny process-info*)
(allow process-fork process-exec)
(allow signal (target same-sandbox))
(allow sysctl-read
  (sysctl-name-regex #"^hw[.]")
  (sysctl-name "sysctl.name2oid")
  (sysctl-name "kern.ostype") (sysctl-name "kern.osrelease")
  (sysctl-name "kern.osrevision") (sysctl-name "kern.osversion")
  (sysctl-name "kern.version") (sysctl-name "kern.hostname")
  (sysctl-name "kern.boottime") (sysctl-name "kern.argmax")
  (sysctl-name "kern.dyld_flags") (sysctl-name "kern.bootargs")
  (sysctl-name "security.mac.lockdown_mode_state")
  (sysctl-name "vm.shared_region_control"))
(allow file-ioctl)
(allow file-read*
  (literal "/")
  (subpath "/bin") (subpath "/sbin") (subpath "/usr")
  (subpath "/System") (subpath "/opt/homebrew")
  (literal "/private/var/select/sh")
  (literal "/dev/null") (literal "/dev/zero")
  (literal "/dev/random") (literal "/dev/urandom")
  (literal "/dev/tty")
  (subpath (param "WORKSPACE")))
(allow file-write*
  (literal "/dev/null") (literal "/dev/tty")
  (subpath (param "WORKSPACE")))
`
	argv := []string{"-D", "WORKSPACE=" + root}
	// getcwd needs metadata for ancestors, while their contents stay restricted.
	for parent, index := filepath.Dir(root), 0; parent != "/"; parent, index = filepath.Dir(parent), index+1 {
		key := fmt.Sprintf("PARENT_%d", index)
		argv = append(argv, "-D", key+"="+parent)
		profile += fmt.Sprintf("(allow file-read-metadata (literal (param %q)))\n", key)
	}
	argv = append(argv, "-p", profile, resolved)
	argv = append(argv, args...)
	clean := make([]string, 0, len(env)+3)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "HOME" && key != "USERPROFILE" && key != "TMPDIR" {
			clean = append(clean, entry)
		}
	}
	clean = append(clean, "HOME="+root, "USERPROFILE="+root, "TMPDIR="+root)
	return launcher, argv, clean, nil
}
