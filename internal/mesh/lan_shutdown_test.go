package mesh

import (
	"crypto/tls"
	"net"
	"testing"
)

// On shutdown the agent's HTTP server closes the LAN listener, and the runtime closes it again before the
// network is closed. The second close must not fail a clean shutdown.
func Test_LANCloseAfterListenerAlreadyClosedIsClean(t *testing.T) {
	self, _ := LoadCertificate(t.TempDir(), "self")
	trust, _ := OpenTrust(t.TempDir())
	raw, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	l := &LAN{name: "self", cert: self, trust: trust, peers: map[string]lanPeer{}, done: make(chan struct{}), cancel: func() {}}
	close(l.done)
	l.listener = tls.NewListener(raw, trust.TLS(self, "", true))
	if e = l.listener.Close(); e != nil {
		t.Fatal(e)
	}
	if e = l.Close(); e != nil {
		t.Fatalf("a listener that is already closed is not a failed shutdown: %v", e)
	}
}
