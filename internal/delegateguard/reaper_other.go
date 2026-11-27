//go:build !linux

package delegateguard

import "os/exec"

const ReaperMarker = "__delegate-reaper"

// ReaperPipes is a stub on non-Linux platforms where WrapCommand returns ok=false.
type ReaperPipes struct{}

func (*ReaperPipes) Apply(*exec.Cmd) {}
func (*ReaperPipes) CloseChildEnds() {}
func (*ReaperPipes) CloseAll()       {}
func (*ReaperPipes) WaitStart() error {
	return nil
}
func (*ReaperPipes) CloseDeath() {}

func EnableSubreaper() {}

func SubreaperEnabled() bool { return false }

func HandleReaper([]string) {}

func WrapCommand(string, []string) (string, []string, *ReaperPipes, bool) {
	return "", nil, nil, false
}

func RunReaper([]string) int { return 2 }
