package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/control"
	"golang.org/x/sys/windows"
)

func Test_KernelStartRejectsEndpointBeforeRestart(t *testing.T) {
	dir := t.TempDir()
	l, bound, err := control.Listen(control.ListenOptions{StateDir: dir, Node: "restart-freshness-test"})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = server.Serve(l) }()
	defer server.Close()
	started := time.Now()
	endpoint := v2.ControlEndpointFile{APIVersion: v2.APIVersion, PID: os.Getpid(), Transport: bound.Transport, Endpoint: bound.Address, StartedAt: v2.At(started.Add(-time.Second))}
	if err := control.WriteEndpoint(dir, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := waitKernelStart(dir, control.Endpoint{}, started, 20*time.Millisecond); err == nil {
		t.Fatal("reachable endpoint from before the restart was accepted")
	}
	endpoint.StartedAt = v2.At(started)
	if err := control.WriteEndpoint(dir, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := waitKernelStart(dir, control.Endpoint{}, started, time.Second); err != nil {
		t.Fatal("fresh endpoint at serialized millisecond precision was rejected", err)
	}
}

func Test_TaskStateDirectory(t *testing.T) {
	for _, tc := range []struct{ name, args, cwd, want string }{
		{"quoted", `serve --state-dir "D:\O'Brien & 中文\state"`, `C:\install`, `D:\O'Brien & 中文\state`},
		{"equals", `serve --state-dir="D:\state dir"`, `C:\install`, `D:\state dir`},
		{"relative", `serve --state-dir "node state"`, `D:\workspace`, `D:\workspace\node state`},
		{"last flag", `serve --state-dir D:\first --state-dir D:\last`, `C:\install`, `D:\last`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc strings.Builder
			// Escaping the command line also exercises XML entity decoding before argument parsing.
			doc.WriteString(`<Task><Actions><Exec><Arguments>`)
			doc.WriteString(strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(tc.args))
			doc.WriteString(`</Arguments><WorkingDirectory>` + tc.cwd + `</WorkingDirectory></Exec></Actions></Task>`)
			got, err := taskStateDir(doc.String(), "xmesh-node")
			if err != nil || got != tc.want {
				t.Fatal(got, tc.want, err)
			}
		})
	}
	got, err := taskStateDir(`<Task><Actions><Exec><Arguments>serve</Arguments></Exec></Actions></Task>`, "xmesh-node")
	want, _ := app.StateDir("node")
	if err != nil || got != want {
		t.Fatal(got, want, err)
	}
}

func Test_TaskStateDirectoryFollowsRenamedConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "custom config.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(cliConfig, "cli-node", "renamed-node", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	want, _ := app.StateDir("renamed-node")
	for _, args := range []string{
		windows.ComposeCommandLine([]string{"serve", "--config", file}),
		windows.ComposeCommandLine([]string{"serve", "--config=custom config.yaml"}),
	} {
		document := `<Task><Actions><Exec><Arguments>` + strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(args) + `</Arguments><WorkingDirectory>` + dir + `</WorkingDirectory></Exec></Actions></Task>`
		got, err := taskStateDir(document, "xmesh-original-name")
		if err != nil || got != want {
			t.Fatal(got, want, err)
		}
	}
}

func Test_KernelStartRequiresReachableEndpoint(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	endpoint := control.Endpoint{ControlEndpointFile: v2.ControlEndpointFile{PID: os.Getpid(), Transport: v2.TransportPipe, Endpoint: `\\.\pipe\xmesh-test-unavailable`}}
	endpoint.StartedAt.Time = started
	if err := control.WriteEndpoint(dir, endpoint.ControlEndpointFile); err != nil {
		t.Fatal(err)
	}
	if err := waitKernelStart(dir, control.Endpoint{}, started, 20*time.Millisecond); err == nil {
		t.Fatal("unreachable endpoint accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, control.FileName)); err != nil {
		t.Fatal(err)
	}
	// Validate command-line quoting using the same Windows routine used during installation.
	args := []string{"serve", "--state-dir", `D:\space dir\`}
	parsed, err := windows.DecomposeCommandLine(windows.ComposeCommandLine(args))
	if err != nil || len(parsed) != len(args) || parsed[2] != args[2] {
		t.Fatal(parsed, err)
	}
}

func Test_KernelStartAcceptsResponsiveOlderControl(t *testing.T) {
	dir := t.TempDir()
	l, bound, err := control.Listen(control.ListenOptions{StateDir: dir, Node: "start-older-test"})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(v2.HeaderVersion, "1.7")
		w.WriteHeader(http.StatusNotFound)
	})}
	go func() { _ = server.Serve(l) }()
	defer server.Close()
	endpoint := v2.ControlEndpointFile{PID: os.Getpid(), Transport: bound.Transport, Endpoint: bound.Address, StartedAt: v2.At(time.Now())}
	if err := control.WriteEndpoint(dir, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := waitKernelStart(dir, control.Endpoint{}, time.Time{}, time.Second); err != nil {
		t.Fatal("a responsive older kernel must satisfy idempotent CLI start", err)
	}
}
