//go:build linux

package ci

import (
	"os"
	"os/exec"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// platformNotices warns when the user's services stop at logout: without lingering, the kernel
// and the runners under it end when the last session closes.
func platformNotices() []v2.CiNotice {
	if _, err := exec.LookPath("loginctl"); err != nil {
		return nil
	}
	out, err := exec.Command("loginctl", "show-user", "--property=Linger", "--value", os.Getenv("USER")).Output()
	if err != nil || strings.TrimSpace(string(out)) != "no" {
		return nil
	}
	return []v2.CiNotice{{
		Code:    v2.CiNoticeLingerDisabled,
		Message: "退出登录后用户服务会停止，运行器也会停。",
		Command: "loginctl enable-linger $USER",
	}}
}
