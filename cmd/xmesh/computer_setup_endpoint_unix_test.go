//go:build !windows

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/tzf1003/xmesh/internal/control"
)

func TestComputerSetupAuthorizesOnlyTheConfiguredEndpoint(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xmc-setup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, bound, err := control.Listen(control.ListenOptions{StateDir: dir, Node: "computer"})
	if err != nil {
		t.Fatal(err)
	}
	var requested atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/authorize" {
			requested.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	// This helper does not exist; direct custom-Host setup must not install it.
	missingState := filepath.Join(dir, "uninstalled")
	if err := computerService("setup", []string{"--endpoint", bound.Address, "--state-dir", missingState, "--helper", filepath.Join(dir, "missing-helper")}); err != nil {
		t.Fatal(err)
	}
	if requested.Load() != 1 {
		t.Fatal("wrong Host authorization count", requested.Load())
	}
	if _, err := os.Stat(missingState); !os.IsNotExist(err) {
		t.Fatal("setup created unrelated service state", err)
	}
	_ = server.Close()
	if err := computerService("setup", []string{"--endpoint", bound.Address, "--state-dir", missingState}); err == nil {
		t.Fatal("missing custom Host was accepted")
	}
	if _, err := os.Stat(missingState); !os.IsNotExist(err) {
		t.Fatal("missing custom Host installed default service", err)
	}
}

func TestComputerSetupEndpointFlagCannotBeSilentlyIgnoredByServiceActions(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "install", "uninstall"} {
		if err := computerService(action, []string{"--endpoint", "/tmp/custom-host.sock"}); err == nil {
			t.Fatal(action, "ignored endpoint")
		}
	}
}
