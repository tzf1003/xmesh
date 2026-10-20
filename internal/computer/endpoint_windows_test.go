package computer

import "testing"

func TestWindowsComputerEndpointShape(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		valid    bool
	}{
		{`\\.\pipe\xmesh-S-1-5-21-1-2-3-1001-computer`, true},
		{`\\.\pipe\xmesh-S-1-5-21-1-2-3-1001`, false},
		{`\\.\pipe\xmesh-invalid-computer`, false},
		{`\\server\pipe\xmesh-S-1-5-21-1-2-3-1001-computer`, false},
		{`C:\computer.sock`, false},
	} {
		if ValidEndpoint(tc.endpoint) != tc.valid {
			t.Fatalf("endpoint %q valid=%v", tc.endpoint, tc.valid)
		}
	}
}
