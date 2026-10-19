package onboarding

import (
	"github.com/tzf1003/xmesh/internal/privdir"
	"golang.org/x/sys/windows"
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
	var overlap windows.Overlapped
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap); e != nil {
		f.Close()
		return nil, problem("busy", "该设备正在配置，请稍后查询任务进度", "node_name")
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlap); _ = f.Close() }, nil
}
