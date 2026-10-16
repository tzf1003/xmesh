//go:build windows

package control

import (
	"fmt"
	"net"
	"strings"

	winio "github.com/tailscale/go-winio"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"golang.org/x/sys/windows"
)

// Listen creates \\.\pipe\xmesh-<sid>-<node>. The pipe is created with the first-
// instance flag (so another process cannot squat the name), rejects remote
// clients, and carries a protected DACL naming only the kernel's user, SYSTEM,
// Administrators and the extra SIDs. AppContainer packages are not in it, so L2
// sandboxes cannot open the pipe (CON-SEC-12).
func Listen(o ListenOptions) (net.Listener, Bound, error) {
	sid, err := CurrentSID()
	if err != nil {
		return nil, Bound{}, err
	}
	sddl, err := PipeSDDL(sid, o.AllowSIDs)
	if err != nil {
		return nil, Bound{}, err
	}
	name := PipeName(sid, o.Node)
	l, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: sddl, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
	if err != nil {
		return nil, Bound{}, err
	}
	return l, Bound{Transport: v1.TransportPipe, Address: name}, nil
}

func PipeName(sid, node string) string { return `\\.\pipe\xmesh-` + sid + "-" + node }

// CurrentSID is the string SID of the user this process runs as.
func CurrentSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

// PipeSDDL builds the pipe's security descriptor. Every SID is validated first, so
// a malformed value cannot add ACEs to the string.
func PipeSDDL(own string, extra []string) (string, error) {
	var b strings.Builder
	b.WriteString("D:P")
	seen := map[string]bool{}
	for _, s := range append([]string{own, "S-1-5-18", "S-1-5-32-544"}, extra...) {
		// StringToSid also accepts SDDL aliases such as WD (Everyone) or AU, so only the
		// numeric form is let through, and it must round-trip unchanged.
		sid, err := windows.StringToSid(s)
		if err != nil || !strings.HasPrefix(s, "S-1-") || !strings.EqualFold(sid.String(), s) {
			return "", fmt.Errorf("SID 无效: %q", s)
		}
		if broadSID(sid.String()) {
			return "", fmt.Errorf("不允许把控制通道开放给宽泛的主体: %s", s)
		}
		if canonical := sid.String(); !seen[canonical] {
			seen[canonical] = true
			b.WriteString("(A;;GA;;;" + canonical + ")")
		}
	}
	return b.String(), nil
}

// broadSID reports principals that stand for many users or for sandboxed code: Everyone,
// anonymous, Authenticated Users, Users, and AppContainer packages and capabilities.
func broadSID(s string) bool {
	switch s {
	case "S-1-1-0", "S-1-5-7", "S-1-5-11", "S-1-5-32-545", "S-1-5-32-546":
		return true
	}
	return strings.HasPrefix(s, "S-1-15-")
}

// connectionIdentity names the pipe's user in the audit record. A pipe does not tell the server
// which of the allowed principals connected, so this is the kernel's own SID.
func connectionIdentity() string {
	if sid, err := CurrentSID(); err == nil {
		return sid
	}
	return "unknown"
}
