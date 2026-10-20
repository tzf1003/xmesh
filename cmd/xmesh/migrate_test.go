package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

func TestMigrateBacksUpConfigurationAndLegacyTrust(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "xmesh.yaml")
	raw := []byte(cliConfig)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(state, "lan"), 0700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"legacy":"paired-device"}`)
	if err := os.WriteFile(filepath.Join(state, "lan", "peers.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := output(t, func() error { return migrate([]string{"--config", file, "--state-dir", state}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(file)
	if err != nil || cfg.SchemaVersion != 2 {
		t.Fatal(cfg, err)
	}
	backups, err := filepath.Glob(file + ".pre-device-v2-*")
	if err != nil {
		t.Fatal(err)
	}
	var backup string
	for _, path := range backups {
		info, _ := os.Stat(path)
		if info.Mode().IsRegular() {
			backup = path
		}
	}
	got, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("source backup missing", err)
	}
	got, err = os.ReadFile(filepath.Join(backup+".state", "lan", "peers.json"))
	if err != nil || !bytes.Equal(got, legacy) {
		t.Fatal("legacy trust backup missing", err)
	}
	got, _ = os.ReadFile(filepath.Join(state, "lan", "peers.json"))
	if !bytes.Equal(got, legacy) {
		t.Fatal("legacy source trust changed")
	}
	if _, err := output(t, func() error { return migrate([]string{"--config", file, "--state-dir", state}) }); err != nil {
		t.Fatal(err)
	}
}
