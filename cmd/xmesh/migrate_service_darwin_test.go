package main

import (
	"encoding/xml"
	"github.com/tzf1003/xmesh/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationMissingLaunchAgentIsNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := readInstalledMigrationPaths("xmesh-missing")
	if !os.IsNotExist(err) {
		t.Fatalf("expected missing registration, got %v", err)
	}
}

func TestMigrationReadsActualLaunchAgentPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, state := filepath.Join(home, "custom 配置.yaml"), filepath.Join(home, "state")
	escape := func(s string) string {
		var out strings.Builder
		_ = xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	document := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>xmesh-office</string><key>ProgramArguments</key><array><string>/Applications/xmesh</string><string>serve</string><string>--service</string><string>--config</string><string>` + escape(file) + `</string><string>--state-dir</string><string>` + escape(state) + `</string></array></dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "xmesh-office.plist"), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readInstalledMigrationPaths("xmesh-office")
	if err != nil || got.config != file || got.state != state {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(file, []byte(cliConfig), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := output(t, func() error { return migrate([]string{"--name", "xmesh-office"}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(file)
	if err != nil || cfg.SchemaVersion != 2 {
		t.Fatal(cfg, err)
	}
}
