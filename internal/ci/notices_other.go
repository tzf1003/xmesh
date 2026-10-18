//go:build !linux && !windows

package ci

import v1 "github.com/tzf1003/xmesh/api/control/v1"

func platformNotices() []v1.CiNotice { return nil }
