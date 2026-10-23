//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A unit file the library could not disable is removed, with its enable link, and the manager reloads.
func TestRemoveUserUnitDeletesUnitAndLink(t *testing.T) {
	config, bin := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n[ \"$2\" = disable ] && exit 1\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	dir := filepath.Join(config, "systemd", "user")
	if err := os.MkdirAll(filepath.Join(dir, "default.target.wants"), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, "xmesh-n.service")
	link := filepath.Join(dir, "default.target.wants", "xmesh-n.service")
	for _, f := range []string{unit, link} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !userUnitExists("xmesh-n") || userUnitExists("xmesh-other") {
		t.Fatal("userUnitExists must follow the unit file")
	}
	if err := removeUserUnit("xmesh-n"); err != nil {
		t.Fatalf("a failing disable must not stop the removal: %v", err)
	}
	for _, f := range []string{unit, link} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone: %v", f, err)
		}
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "--user daemon-reload") {
		t.Fatalf("the manager must reload, calls: %s", calls)
	}
	if userUnitExists("xmesh-n") {
		t.Fatal("unit should no longer exist")
	}
	// Nothing left to remove is not an error either.
	if err := removeUserUnit("xmesh-n"); err != nil {
		t.Fatal(err)
	}
}
