package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/control"
	"golang.org/x/sys/windows"
)

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

func Test_KernelStartRequiresReachableEndpoint(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	endpoint := control.Endpoint{ControlEndpointFile: v1.ControlEndpointFile{PID: os.Getpid(), Transport: v1.TransportPipe, Endpoint: `\\.\pipe\xmesh-test-unavailable`}}
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
