package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/delegateguard"
)

const ctlProbeMarker = "--xmesh-ctl-redelegate-probe"

func TestMain(m *testing.M) {
	delegateguard.HandleReaper(os.Args)
	if len(os.Args) >= 4 && os.Args[1] == ctlProbeMarker {
		os.Exit(runControlProbe(os.Args[2], os.Args[3] == "spawn"))
	}
	os.Exit(m.Run())
}

func runControlProbe(endpointJSON string, spawnChild bool) int {
	_ = os.Unsetenv("XMESH_DELEGATE")
	if spawnChild {
		exe, err := os.Executable()
		if err != nil {
			return 2
		}
		if out, err := exec.Command(exe, ctlProbeMarker, endpointJSON, "direct").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "child control probe failed: %v (%s)\n", err, out)
			return 3
		}
	}

	var file v2.ControlEndpointFile
	if err := json.Unmarshal([]byte(endpointJSON), &file); err != nil {
		fmt.Fprintln(os.Stderr, "unmarshal endpoint:", err)
		return 7
	}
	client := NewClient(Endpoint{ControlEndpointFile: file})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reqs := []struct {
		method string
		path   string
		body   any
	}{
		{"GET", "/v2/status", nil},
		{"GET", "/v2/delegate", nil},
		{"GET", "/v2/config", nil},
		{"POST", "/v2/pats", map[string]any{"name": "stolen", "scopes": []string{"exec:*"}}},
	}
	for _, rq := range reqs {
		resp, err := client.Do(ctx, rq.method, rq.path, rq.body, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %s error: %v\n", rq.method, rq.path, err)
			return 4
		}
		if resp.Status != v2.ErrForbidden.HTTPStatus() {
			fmt.Fprintf(os.Stderr, "%s %s status=%d body=%s, want 403\n", rq.method, rq.path, resp.Status, resp.Body)
			return 5
		}
		var e v2.Error
		if err := json.Unmarshal(resp.Body, &e); err != nil || e.Code != v2.ErrForbidden {
			fmt.Fprintf(os.Stderr, "%s %s code=%q body=%s, want forbidden\n", rq.method, rq.path, e.Code, resp.Body)
			return 6
		}
	}
	return 0
}

// conformance: CTR-DLG-06
func Test_DelegatedSubprocessCannotAccessControlChannel(t *testing.T) {
	guard := delegateguard.New()
	f := newFixture(t, func(o *Options) {
		o.Delegate = fakeDelegate{}
		o.DelegateGuard = guard
	})

	// Normal caller can read /v2/status and /v2/delegate.
	f.ok("GET", "/v2/status", nil, "StatusResponse")
	f.ok("GET", "/v2/delegate", nil, "DelegateState")

	epBytes, err := json.Marshal(f.server.Endpoint().ControlEndpointFile)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, ctlProbeMarker, string(epBytes), "spawn")
	cmd.Env = append(os.Environ(), "XMESH_DELEGATE=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	doneStart := guard.BeginStart()
	if err := cmd.Start(); err != nil {
		doneStart()
		t.Fatal(err)
	}
	untrack := guard.Track(cmd.Process.Pid, 0)
	doneStart()
	err = cmd.Wait()
	untrack()
	if err != nil {
		t.Fatalf("delegated control probe failed: %v (%s)", err, stderr.String())
	}

	// Normal caller still works after untrack.
	f.ok("GET", "/v2/status", nil, "StatusResponse")
	f.ok("GET", "/v2/delegate", nil, "DelegateState")
}
