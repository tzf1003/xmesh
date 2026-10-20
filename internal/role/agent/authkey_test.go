package agent

import (
	"github.com/tzf1003/xmesh/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthkeyReadabilityUsesV2TailnetConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "authkey")
	if err := os.WriteFile(file, []byte("test-key"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SchemaVersion: 2}
	cfg.Mesh.Tailnet.Enabled = true
	cfg.Mesh.Tailnet.AuthkeyFile = file
	if !authkeyReadable(cfg) {
		t.Fatal("v2 key reported unreadable")
	}
	cfg.Mesh.Tailnet.AuthMethod = "browser"
	if authkeyReadable(cfg) {
		t.Fatal("browser login used a stale key file")
	}
	cfg.Mesh.Tailnet.AuthMethod = ""
	cfg.Mesh.Tailnet.Enabled = false
	if authkeyReadable(cfg) {
		t.Fatal("disabled path reported readable")
	}
	cfg.SchemaVersion = 1
	cfg.Mesh.Mode = "tailnet"
	cfg.Mesh.AuthkeyFile = file
	if !authkeyReadable(cfg) {
		t.Fatal("legacy key reported unreadable")
	}
}
