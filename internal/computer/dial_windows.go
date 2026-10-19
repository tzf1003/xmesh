package computer

import (
	"context"
	"fmt"
	"github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
	"net"
	"strings"
)

func DefaultEndpoint() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return `\\.\pipe\xmesh-` + u.User.Sid.String() + `-computer`
}
func EndpointForState(string) string { return DefaultEndpoint() }
func dialDesktop(ctx context.Context, endpoint string) (net.Conn, error) {
	if !strings.HasPrefix(endpoint, `\\.\pipe\xmesh-`) || !strings.HasSuffix(endpoint, "-computer") {
		return nil, fmt.Errorf("desktop endpoint must be a local computer pipe")
	}
	sidText := strings.TrimSuffix(strings.TrimPrefix(endpoint, `\\.\pipe\xmesh-`), "-computer")
	sid, err := windows.StringToSid(sidText)
	if err != nil || sid.String() != sidText {
		return nil, fmt.Errorf("desktop endpoint SID is invalid")
	}
	c, err := winio.DialPipeContext(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("desktop pipe has no identity handle")
	}
	var pid uint32
	if err = windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		_ = c.Close()
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		_ = c.Close()
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || !windows.EqualSid(user.User.Sid, sid) {
		_ = c.Close()
		return nil, fmt.Errorf("desktop executor identity mismatch")
	}
	return c, nil
}
