//go:build windows

package ci

import (
	"golang.org/x/sys/windows/registry"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// platformNotices warns when Windows does not sign in by itself: the kernel runs as a task of the
// logged-in user, so after a reboot nothing runs until someone signs in.
func platformNotices() []v2.CiNotice {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	if value, _, err := key.GetStringValue("AutoAdminLogon"); err == nil && value == "1" {
		return nil
	}
	return []v2.CiNotice{{
		Code:    v2.CiNoticeAutologonDisabled,
		Message: "没有开启自动登录，重启电脑后要先登录，运行器才会恢复。",
	}}
}
