package policy

import (
	"path/filepath"
	"sync"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// Protected paths are files an L2 workspace must never expose. The control
// channel's socket is one: a sandboxed process that could see it could drive the
// node's own control API (CON-SEC-12). bwrap binds the whole workspace, so a
// workspace that contains the socket is refused rather than partly masked.
var protected struct {
	sync.Mutex
	paths map[string]int
}

// ProtectPath registers path for the life of the process and returns a function
// that releases it. Symlinks in the parent directory are resolved, as the sandbox
// launchers compare resolved workspace paths.
func ProtectPath(path string) (release func()) {
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(resolved, filepath.Base(path))
	}
	protected.Lock()
	defer protected.Unlock()
	if protected.paths == nil {
		protected.paths = map[string]int{}
	}
	protected.paths[path]++
	return func() {
		protected.Lock()
		defer protected.Unlock()
		if protected.paths[path]--; protected.paths[path] <= 0 {
			delete(protected.paths, path)
		}
	}
}

func protectedWorkspace(root string) error {
	protected.Lock()
	defer protected.Unlock()
	for path := range protected.paths {
		if within(root, path) || within(path, root) {
			return toolerr.New("policy_denied", "L2 工作目录包含本机控制通道", "rule", "sandbox.control_channel", "reason", "请选择不包含节点状态目录的独立工作目录")
		}
	}
	return nil
}
