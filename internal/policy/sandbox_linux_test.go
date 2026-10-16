package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

func Test_L2RuntimeIsolation(t *testing.T) {
	if os.Getenv("XMESH_TEST_L2") != "1" {
		t.Skip("set XMESH_TEST_L2=1 on a Linux host with bubblewrap and user namespaces")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if e := os.Mkdir(workspace, 0700); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(root, "outside")
	if e := os.WriteFile(outside, []byte("host-only"), 0600); e != nil {
		t.Fatal(e)
	}
	engine, _ := New(config.Policy{Isolation: "L2"})
	env, e := engine.Environment(nil)
	if e != nil {
		t.Fatal(e)
	}
	command := `test ! -e "$1" && test "$HOME" = /tmp && printf sandbox > created && test -r /proc/net/route && test "$(wc -l < /proc/net/route)" -eq 1 && printf ISOLATED`
	launch, args, env, e := engine.Sandbox("sh", []string{"-c", command, "sh", outside}, workspace, env)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(launch, args...)
	cmd.Env = env
	output, e := cmd.CombinedOutput()
	if e != nil || strings.TrimSpace(string(output)) != "ISOLATED" {
		t.Fatalf("isolation launch: %v %s", e, output)
	}
	if data, e := os.ReadFile(filepath.Join(workspace, "created")); e != nil || string(data) != "sandbox" {
		t.Fatal("workspace write", e)
	}
}
