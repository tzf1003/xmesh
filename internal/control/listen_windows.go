//go:build windows

package control

import (
	"fmt"
	"net"
	"strings"

	winio "github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
)

// Listen creates \\.\pipe\xmesh-<sid>-<node>. The pipe is created with the first-
// instance flag (so another process cannot squat the name), rejects remote
// clients, and carries a protected DACL naming only the kernel's user, SYSTEM,
// Administrators and the extra SIDs. AppContainer packages are not in it, so L2
// sandboxes cannot open the pipe (CON-SEC-12).
func Listen(o ListenOptions) (net.Listener, Endpoint, error) {
	sid, err := CurrentSID()
	if err != nil {
		return nil, Endpoint{}, err
	}
	sddl, err := PipeSDDL(sid, o.AllowSIDs)
	if err != nil {
		return nil, Endpoint{}, err
	}
	name := PipeName(sid, o.Node)
	l, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: sddl, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
	if err != nil {
		return nil, Endpoint{}, err
	}
	return l, Endpoint{Transport: "pipe", Address: name}, nil
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
		sid, err := windows.StringToSid(s)
		if err != nil {
			return "", fmt.Errorf("SID 无效: %w", err)
		}
		if canonical := sid.String(); !seen[canonical] {
			seen[canonical] = true
			b.WriteString("(A;;GA;;;" + canonical + ")")
		}
	}
	return b.String(), nil
}
