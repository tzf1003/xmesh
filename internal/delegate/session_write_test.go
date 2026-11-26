package delegate

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"testing/synctest"
)

type writeErrorConn struct {
	net.Conn
}

func (writeErrorConn) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// conformance: CTR-DLG-04
func Test_CTRDLG04_WriteInjectionFailureReasons(t *testing.T) {
	cases := []struct {
		reason    string
		failWrite bool
		err       error
	}{
		{reason: "write_timeout", err: os.ErrDeadlineExceeded},
		{reason: "write_failed", failWrite: true, err: io.ErrClosedPipe},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn, peer := net.Pipe()
				defer conn.Close()
				defer peer.Close()
				// The open peer never reads, so a normal write blocks until the virtual clock reaches the deadline.
				if tc.failWrite {
					conn = writeErrorConn{conn}
				}

				reason, err := writeInjection(conn, []byte("message"))
				if reason != tc.reason || !errors.Is(err, tc.err) {
					t.Fatalf("writeInjection returned (%q, %v), want (%q, %v)", reason, err, tc.reason, tc.err)
				}
			})
		})
	}
}
