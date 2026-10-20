package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceInfoRetainsInstallOptions(t *testing.T) {
	previous := installedMigrationPaths
	t.Cleanup(func() { installedMigrationPaths = previous })
	root := t.TempDir()
	password := filepath.Join(root, "private password")
	installedMigrationPaths = func(name string) (migrationPaths, error) {
		if name != "xmesh-original" {
			t.Fatal(name)
		}
		return migrationPaths{config: filepath.Join(root, "xmesh.yaml"), state: root, password: password, localOnly: true}, nil
	}
	out, err := captureStdout(func() error { return run([]string{"service-info", "--name", "xmesh-original", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Messages []string }
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || len(envelope.Messages) != 1 {
		t.Fatal(out, err)
	}
	var info struct {
		Password  string `json:"oauth_password_file"`
		LocalOnly bool   `json:"local_only"`
	}
	if err := json.Unmarshal([]byte(envelope.Messages[0]), &info); err != nil || info.Password != password || !info.LocalOnly {
		t.Fatal(info, err)
	}
	installedMigrationPaths = func(string) (migrationPaths, error) { return migrationPaths{}, os.ErrNotExist }
	out, err = captureStdout(func() error { return run([]string{"service-info", "--name", "xmesh-original", "--json"}) })
	if err == nil {
		t.Fatal("missing service accepted")
	}
	var failure struct {
		Error struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(out), &failure) != nil || failure.Error.Reason != "not_installed" {
		t.Fatal(out)
	}
}
