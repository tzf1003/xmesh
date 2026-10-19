//go:build !windows

package onboarding

import (
	"github.com/tzf1003/xmesh/internal/privdir"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func (s *Store) lock(node string) (func(), error) {
	dir := filepath.Join(s.Root, "setup", "locks")
	if e := privdir.Ensure(dir); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, node+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, problem("busy", "该设备正在配置，请稍后查询任务进度", "node_name")
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
