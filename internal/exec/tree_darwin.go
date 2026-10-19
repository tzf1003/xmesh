//go:build darwin

package exec

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func treeSnapshot() ([]treeIdentity, error) {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	result := make([]treeIdentity, 0, len(all))
	for _, p := range all {
		if p.Proc.P_stat == 5 {
			continue
		} // SZOMB no longer executes or owns userspace handles.
		result = append(result, treeIdentity{int(p.Proc.P_pid), int(p.Eproc.Ppid), int(p.Eproc.Pgid), fmt.Sprintf("%d:%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec)})
	}
	return result, nil
}
