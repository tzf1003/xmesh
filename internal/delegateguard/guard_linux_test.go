//go:build linux

package delegateguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func handlePlatformTestHook(args []string) {
	if len(args) >= 3 && args[1] == "--guard-test-unix-http" {
		_ = os.Unsetenv("XMESH_DELEGATE")
		sock := args[2]
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sock)
				},
			},
		}
		resp, err := client.Get("http://unix/")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 204 {
			os.Exit(3)
		}
		os.Exit(0)
	}
	if len(args) >= 4 && args[1] == "--guard-test-double-fork" {
		_ = os.Unsetenv("XMESH_DELEGATE")
		exe, err := os.Executable()
		if err != nil {
			os.Exit(4)
		}
		gc := exec.Command(exe, "--guard-test-df-grandchild", args[2], args[3])
		gc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
		// Exit intermediate parent immediately so grandchild is reparented to the subreaper.
		os.Exit(0)
	}
	if len(args) >= 4 && args[1] == "--guard-test-df-grandchild" {
		_ = os.Unsetenv("XMESH_DELEGATE")
		// Wait briefly so intermediate parent exits before we connect.
		time.Sleep(80 * time.Millisecond)
		resp, err := http.Get(args[2])
		if err != nil || resp.StatusCode != 204 {
			os.Exit(6)
		}
		_ = resp.Body.Close()
		for {
			f, err := os.OpenFile(args[3], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				os.Exit(7)
			}
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Close()
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func Test_GuardCatchesLinuxSubreaperDoubleForkSetsidAndUnixSocket(t *testing.T) {
	EnableSubreaper()
	g := New()

	// 1. Unix domain socket peer-cred detection (use short path under /tmp to stay within 108B sun_path).
	sockDir, err := os.MkdirTemp("/tmp", "xg-")
	if err != nil {
		sockDir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	}
	sockPath := filepath.Join(sockDir, "c.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var sawUnix atomic.Bool
	srv := &http.Server{
		ConnContext: ConnContext(g),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawUnix.Store(IsDelegatedHTTPRequest(g, r))
			w.WriteHeader(204)
		}),
	}
	defer srv.Close()
	go func() { _ = srv.Serve(ln) }()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	untracked := exec.Command(exe, "--guard-test-unix-http", sockPath)
	if err := untracked.Run(); err != nil {
		t.Fatal(err)
	}
	if sawUnix.Load() {
		t.Fatal("untracked Unix socket caller was marked delegated")
	}

	runTrackedProbe(t, g, "--guard-test-unix-http", sockPath)
	if !sawUnix.Load() {
		t.Fatal("tracked Unix socket caller was not marked delegated")
	}

	// 2. Subreaper catches double-fork + setsid reparented grandchild even after XMESH_DELEGATE is unset.
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()
	var sawTCP atomic.Bool
	hitCh := make(chan struct{}, 1)
	tcpSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawTCP.Store(IsDelegatedHTTPRequest(g, r))
			w.WriteHeader(204)
			select {
			case hitCh <- struct{}{}:
			default:
			}
		}),
	}
	defer tcpSrv.Close()
	go func() { _ = tcpSrv.Serve(tcpLn) }()

	heartbeat := filepath.Join(t.TempDir(), "df-beat.txt")
	wl, wa, rp, ok := WrapCommand(exe, []string{"--guard-test-double-fork", "http://" + tcpLn.Addr().String(), heartbeat})
	if !ok {
		t.Fatal("WrapCommand returned ok=false")
	}
	cmd := exec.Command(wl, wa...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{rp.DeathRead, rp.StartWrite}
	doneStart := g.BeginStart()
	if err := cmd.Start(); err != nil {
		doneStart()
		rp.CloseAll()
		t.Fatal(err)
	}
	rp.CloseChildEnds()
	untrack := g.Track(cmd.Process.Pid, 0)
	doneStart()
	if err := rp.WaitStart(); err != nil {
		rp.CloseDeath()
		_ = cmd.Wait()
		untrack()
		t.Fatal(err)
	}

	select {
	case <-hitCh:
	case <-time.After(5 * time.Second):
		rp.CloseDeath()
		_ = cmd.Wait()
		untrack()
		t.Fatal("timed out waiting for reparented double-fork grandchild HTTP request")
	}
	if !sawTCP.Load() {
		rp.CloseDeath()
		_ = cmd.Wait()
		untrack()
		t.Fatal("reparented double-fork + setsid grandchild was NOT detected as delegated")
	}

	// Closing death pipe terminates the reparented grandchild too.
	rp.CloseDeath()
	_ = cmd.Wait()
	untrack()

	info1, _ := os.Stat(heartbeat)
	time.Sleep(150 * time.Millisecond)
	info2, _ := os.Stat(heartbeat)
	if info1 != nil && info2 != nil && info2.Size() > info1.Size() {
		t.Fatal("reparented double-fork grandchild kept running after death pipe closed")
	}
}
