package delegateguard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestMain(m *testing.M) {
	HandleReaper(os.Args)
	if len(os.Args) >= 3 && os.Args[1] == "--guard-test-http" {
		_ = os.Unsetenv("XMESH_DELEGATE")
		resp, err := http.Get(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 204 {
			fmt.Fprintf(os.Stderr, "status=%d\n", resp.StatusCode)
			os.Exit(3)
		}
		os.Exit(0)
	}
	if len(os.Args) >= 3 && os.Args[1] == "--guard-test-grandchild-http" {
		_ = os.Unsetenv("XMESH_DELEGATE")
		exe, err := os.Executable()
		if err != nil {
			os.Exit(4)
		}
		cmd := exec.Command(exe, "--guard-test-http", os.Args[2])
		if err := cmd.Run(); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
	handlePlatformTestHook(os.Args)
	os.Exit(m.Run())
}

func runTrackedProbe(t *testing.T, g *Guard, args ...string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "XMESH_DELEGATE=1")
	doneStart := g.BeginStart()
	if err := cmd.Start(); err != nil {
		doneStart()
		t.Fatal(err)
	}
	untrack := g.Track(cmd.Process.Pid, 0)
	doneStart()
	err = cmd.Wait()
	untrack()
	if err != nil {
		t.Fatalf("tracked probe %v failed: %v", args, err)
	}
}

func Test_GuardTracksSubprocessTCPAndAllowsNormalProcess(t *testing.T) {
	g := New()
	var sawDelegated atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawDelegated.Store(IsDelegatedHTTPRequest(g, r))
		w.WriteHeader(204)
	}))
	defer srv.Close()

	// Normal request from this non-delegated test process is allowed.
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sawDelegated.Load() {
		t.Fatal("non-delegated process was marked delegated")
	}

	// Normal sibling process (not tracked) is also allowed even if XMESH_DELEGATE=1 is spoofed.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sibling := exec.Command(exe, "--guard-test-http", srv.URL)
	sibling.Env = append(os.Environ(), "XMESH_DELEGATE=1")
	if err := sibling.Run(); err != nil {
		t.Fatal(err)
	}
	if sawDelegated.Load() {
		t.Fatal("untracked process was marked delegated via spoofed env")
	}

	// Direct delegated child with XMESH_DELEGATE unset is still detected on loopback TCP.
	runTrackedProbe(t, g, "--guard-test-http", srv.URL)
	if !sawDelegated.Load() {
		t.Fatal("tracked child TCP request was not detected as delegated")
	}

	// Grandchild spawned after child unsets XMESH_DELEGATE is also detected.
	sawDelegated.Store(false)
	runTrackedProbe(t, g, "--guard-test-grandchild-http", srv.URL)
	if !sawDelegated.Load() {
		t.Fatal("tracked grandchild TCP request was not detected as delegated")
	}

	if g.Active() {
		t.Fatal("guard stayed active after untrack")
	}
	if g.IsDelegatedPID(os.Getpid()) {
		t.Fatal("self PID must never be marked delegated")
	}
	_ = runtime.GOOS
}
