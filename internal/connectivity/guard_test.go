package connectivity

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

type fakeConn struct {
	net.Conn
	addr net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.addr }

func conn(ip string) net.Conn {
	return fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 1000}}
}

func TestDirectGuardAllowList(t *testing.T) {
	g := newDirectGuard(config.Direct{AllowIPs: []string{"10.0.0.0/8", "203.0.113.7"}}, time.Now)
	for ip, want := range map[string]bool{"10.1.2.3": true, "203.0.113.7": true, "203.0.113.8": false, "8.8.8.8": false} {
		if got := g.admit(conn(ip)); got != want {
			t.Fatalf("%s: admit = %v, want %v", ip, got, want)
		}
	}
}

func TestDirectGuardCapsPreauthAndBacksOffFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	g := newDirectGuard(config.Direct{MaxPreauth: 3}, func() time.Time { return now })
	a, b, c := conn("1.1.1.1"), conn("2.2.2.2"), conn("3.3.3.3")
	if !g.admit(a) || !g.admit(b) || !g.admit(c) {
		t.Fatal("connections under the cap must be admitted")
	}
	if g.admit(conn("4.4.4.4")) {
		t.Fatal("connection over the global pre-auth cap must be refused")
	}
	g.connState(a, http.StateActive) // authenticated: slot freed, no penalty
	if !g.admit(conn("1.1.1.1")) {
		t.Fatal("a source that authenticated must not be penalised")
	}
	g.connState(b, http.StateClosed) // never sent a request
	if g.admit(conn("2.2.2.2")) {
		t.Fatal("a source whose connection failed must back off")
	}
	now = now.Add(2 * time.Second)
	if !g.admit(conn("2.2.2.2")) {
		t.Fatal("the backoff must expire")
	}
}

func TestDirectGuardPerSourceLimit(t *testing.T) {
	g := newDirectGuard(config.Direct{MaxPreauth: 100}, time.Now)
	for i := 0; i < guardPerIPPreauth; i++ {
		if !g.admit(conn("9.9.9.9")) {
			t.Fatalf("connection %d refused too early", i)
		}
	}
	if g.admit(conn("9.9.9.9")) {
		t.Fatal("one source must not exceed its pre-auth share")
	}
}
