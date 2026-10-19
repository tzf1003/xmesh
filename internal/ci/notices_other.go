//go:build !linux && !windows

package ci

import v2 "github.com/tzf1003/xmesh/api/control/v2"

func platformNotices() []v2.CiNotice { return nil }
