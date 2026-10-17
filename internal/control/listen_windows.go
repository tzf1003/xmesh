//go:build windows

package control

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	return &pipeListener{Listener: l}, Bound{Transport: v1.TransportPipe, Address: name}, nil
}

// pipeListener makes Close reliable on go-winio's listener. Its accept loop retries when a
// client connects and drops again before the server has handled the connection
// (ERROR_NO_DATA). A Close that lands during such an attempt is answered with that same error,
// so the loop starts over, waits for a close request that was already consumed, and Close
// blocks for good. Close requests are therefore repeated until the loop has really stopped; a
// request that arrives after that returns at once.
//
// The wait is bounded. When the loop still has not stopped after pipeCloseWait, Close reports it
// and every later Close returns at once, so one stuck listener costs one wait, not one per call.
// The retries go on in the background until the loop stops or they run out.
type pipeListener struct {
	net.Listener
	once    sync.Once
	stopped chan struct{}
}

var (
	pipeCloseRetry    = 25 * time.Millisecond
	pipeCloseAttempts = 400
	pipeCloseWait     = 2 * time.Second
)

var errPipeNotStopped = errors.New("the control pipe listener did not stop")

// pipeCloseInFlight bounds the Close calls that may be outstanding on a listener that never stops.
// A call whose request was lost stays blocked until the loop stops, and a later call is what
// releases it, so a few are needed; more would only pile up goroutines.
const pipeCloseInFlight = 4

func (l *pipeListener) Close() error {
	first := false
	l.once.Do(func() {
		first = true
		l.stopped = make(chan struct{})
		go l.stop(pipeCloseRetry, pipeCloseAttempts)
	})
	if !first {
		select {
		case <-l.stopped:
			return nil
		default:
			return errPipeNotStopped
		}
	}
	select {
	case <-l.stopped:
		return nil
	case <-time.After(pipeCloseWait):
		return errPipeNotStopped
	}
}

// stop sends close requests until the loop has stopped. Each request runs on its own goroutine,
// because a request that is lost inside go-winio leaves its own call blocked. The retry settings
// are passed in: this loop can outlive Close, and it must not read package variables that a
// later test or caller may change.
func (l *pipeListener) stop(retry time.Duration, attempts int) {
	var once sync.Once
	var inFlight atomic.Int32
	request := func() {
		_ = l.Listener.Close()
		inFlight.Add(-1)
		once.Do(func() { close(l.stopped) })
	}
	send := func() {
		if inFlight.Add(1) > pipeCloseInFlight {
			inFlight.Add(-1)
			return
		}
		go request()
	}
	send()
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-l.stopped:
			return
		case <-time.After(retry):
			send()
		}
	}
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
