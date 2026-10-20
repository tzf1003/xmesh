package main

import (
	"github.com/tzf1003/xmesh/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateUsesInstalledCustomConfiguration(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "custom service.yaml")
	if err := os.WriteFile(file, []byte(cliConfig), 0600); err != nil {
		t.Fatal(err)
	}
	previous := installedMigrationPaths
	t.Cleanup(func() { installedMigrationPaths = previous })
	installedMigrationPaths = func(name string) (migrationPaths, error) {
		if name != "xmesh-test" {
			t.Fatal(name)
		}
		return migrationPaths{config: file, state: state}, nil
	}
	if _, err := output(t, func() error { return migrate([]string{"--name", "xmesh-test"}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(file)
	if err != nil || cfg.SchemaVersion != 2 {
		t.Fatal(cfg, err)
	}
	for _, args := range [][]string{{"--name", "xmesh-test", "--config", file}, {"--name", "xmesh-test", "--state-dir", state}} {
		if err := migrate(args); err == nil {
			t.Fatal("ambiguous migration path accepted", args)
		}
	}
}

func TestMigrationReadsServiceArguments(t *testing.T) {
	file, state := filepath.Join(t.TempDir(), "配置 file.yaml"), t.TempDir()
	for _, args := range [][]string{
		{"xmesh", "serve", "--service", "--config", file, "--state-dir", state, "--oauth-password-file", file, "--local-only"},
		{"xmesh", "serve", "--config=" + file, "--state-dir=" + state, "--oauth-password-file=" + file, "--local-only=true"},
	} {
		got, err := migrationPathsFromArgs(args)
		if err != nil || got.config != file || got.state != state || got.password != file || !got.localOnly {
			t.Fatal(got, err)
		}
	}
	for _, args := range [][]string{{"xmesh", "start", "--config", file}, {"xmesh", "serve", "--config", "relative.yaml"}, {"xmesh", "serve", "--config"}} {
		if _, err := migrationPathsFromArgs(args); err == nil {
			t.Fatal(args)
		}
	}
	command, arguments, err := migrationTaskCommand([]byte(`<?xml version="1.0" encoding="UTF-16"?><Task><Actions><Exec><Command>C:\xmesh.exe</Command><Arguments>serve --config &quot;C:\配置\custom.yaml&quot;</Arguments></Exec></Actions></Task>`))
	if err != nil || command != `C:\xmesh.exe` || arguments != `serve --config "C:\配置\custom.yaml"` {
		t.Fatal(command, arguments, err)
	}
}
