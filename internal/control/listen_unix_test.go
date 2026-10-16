//go:build !windows

package control

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

func get(t *testing.T, path string) (int, error) {
	t.Helper()
	c := NewClient(Endpoint{ControlEndpointFile: v1.ControlEndpointFile{Transport: v1.TransportUnix, Endpoint: path}})
	r, err := c.Do(t.Context(), "GET", "/v1/status", nil, nil)
	if err != nil {
		return 0, err
	}
	return r.Status, nil
}

// Rule CON-SEC-12: only the kernel's own user may use the channel
func Test_CONSEC12_PeerCredentialCheck(t *testing.T) {
	dir := shortTempDir(t)
	serve := func(name string, uid uint32) string {
		path := filepath.Join(dir, name)
		l, err := listenUnix(path, uid)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Close() })
		return path
	}
	own := uint32(os.Geteuid())
	if status, err := get(t, serve("own.sock", own)); err != nil || status != 204 {
		t.Fatalf("own uid refused: %d %v", status, err)
	}
	if status, err := get(t, serve("other.sock", own+1)); err == nil {
		t.Fatalf("a different uid was served: %d", status)
	}
}

func Test_ControlSocketPermissions(t *testing.T) {
	f := newFixture(t)
	if err := os.Chmod(f.server.o.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f.server.Endpoint().Address())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0600 {
		t.Fatalf("socket mode %v", st.Mode())
	}
	if f.server.Endpoint().Transport != v1.TransportUnix || filepath.Base(f.server.Endpoint().Address()) != "control.sock" {
		t.Fatalf("%+v", f.server.Endpoint())
	}
}

// A state directory too deep for sun_path falls back to a private per-user directory.
func Test_ControlLongPathFallback(t *testing.T) {
	deep := t.TempDir()
	for len(deep) < 120 {
		deep = filepath.Join(deep, "deeper-directory-name")
	}
	if err := os.MkdirAll(deep, 0700); err != nil {
		t.Fatal(err)
	}
	l, ep, err := Listen(ListenOptions{StateDir: deep, Node: "fallback-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ep.Address)
	defer l.Close()
	if strings.HasPrefix(ep.Address, deep) || len(ep.Address) > maxSocketPath {
		t.Fatalf("fallback not used: %s", ep.Address)
	}
	st, err := os.Stat(filepath.Dir(ep.Address))
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("fallback directory: %v %v", st, err)
	}
	if owner := st.Sys().(*syscall.Stat_t).Uid; int(owner) != os.Geteuid() {
		t.Fatalf("fallback directory owner %d", owner)
	}
}

func Test_ControlStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.sock")
	// a socket file nobody listens on is replaced
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	live, err := listenUnix(path, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer live.Close()
	// a live one is not taken over
	if _, err = listenUnix(path, uint32(os.Geteuid())); err == nil {
		t.Fatal("took over a live socket")
	}
	// neither is an ordinary file
	file := filepath.Join(dir, "regular")
	if err = os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = listenUnix(file, uint32(os.Geteuid())); err == nil {
		t.Fatal("replaced a regular file")
	}
	time.Sleep(0)
}
