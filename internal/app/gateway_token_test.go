package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// tokenTransport presents the default gateway token the kernel wrote into stateDir. The file is read on
// every request: a test may start connecting before the kernel has written it, and then the request
// simply fails and is retried.
type tokenTransport struct{ stateDir string }

func (tt tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := os.ReadFile(filepath.Join(tt.stateDir, "pats", "default.token"))
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
	return http.DefaultTransport.RoundTrip(r)
}

// gatewayHTTPClient is the client the MCP sessions in these tests use. Every gateway request needs the
// token (CTR-GW-07), and the agent listener in local mode ignores it.
func gatewayHTTPClient(stateDir string) *http.Client {
	return &http.Client{Transport: tokenTransport{stateDir: stateDir}}
}
