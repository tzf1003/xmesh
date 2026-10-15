package policy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

func within(path, root string) bool {
	return path == root || (root == string(filepath.Separator) && filepath.IsAbs(path)) || strings.HasPrefix(path, root+string(filepath.Separator))
}

func credentialWorkspace(root string) error {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return toolerr.New("policy_denied", "无法确定凭证目录", "rule", "sandbox.credentials", "reason", "主目录不可用")
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	for _, name := range []string{".ssh", ".config/gcloud", ".azure", ".aws"} {
		secret := filepath.Join(home, name)
		paths := []string{secret}
		if resolved, err := filepath.EvalSymlinks(secret); err == nil {
			paths = append(paths, resolved)
		}
		for _, path := range paths {
			if within(root, path) || within(path, root) {
				return toolerr.New("policy_denied", "L2 工作目录包含凭证目录", "rule", "sandbox.credentials", "reason", "请选择独立工作目录")
			}
		}
	}
	return nil
}
