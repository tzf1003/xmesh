// Package localendpoint derives local transport addresses without depending on
// the control API or any of its service implementations.
package localendpoint

import (
	"path/filepath"
	"strconv"
)

// MaxUnixSocketPath fits the smallest supported sun_path (104 bytes on macOS).
const MaxUnixSocketPath = 100

func UnixSocketPath(state, node, fallbackRoot string, uid int) string {
	path := filepath.Join(state, "control.sock")
	if len(path) > MaxUnixSocketPath {
		return filepath.Join(fallbackRoot, "xmesh-"+strconv.Itoa(uid), node+".sock")
	}
	return path
}
