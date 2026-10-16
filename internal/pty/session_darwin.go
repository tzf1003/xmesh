package pty

import "golang.org/x/sys/unix"

// The session ID survives its leader, so escalation also finds remaining jobs.
func sessionGroups(session int) []int {
	processes, e := unix.SysctlKinfoProcSlice("kern.proc.all")
	if e != nil {
		return nil
	}
	groups := []int{}
	for _, process := range processes {
		if sid, e := unix.Getsid(int(process.Proc.P_pid)); e == nil && sid == session {
			groups = append(groups, int(process.Eproc.Pgid))
		}
	}
	return groups
}
