package ci

import (
	"os/exec"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// Notices lists the conditions of this machine that keep hosted runners from surviving a logout
// or a reboot, or from checking out code well. They matter only once something is bound.
func Notices(bindings int) []v1.CiNotice {
	var out []v1.CiNotice
	if _, ok := Platform(); !ok {
		out = append(out, v1.CiNotice{Code: v1.CiNoticeRunnerUnsupported, Message: "GitHub 没有提供适用于这个系统和架构的官方运行器。"})
	}
	if bindings == 0 {
		return out
	}
	if _, err := exec.LookPath("git"); err != nil {
		out = append(out, v1.CiNotice{Code: v1.CiNoticeGitMissing, Message: "PATH 里没有 git，actions/checkout 会改用下载压缩包的方式，速度较慢。"})
	}
	return append(out, platformNotices()...)
}
