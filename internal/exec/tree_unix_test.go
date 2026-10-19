//go:build linux || darwin

package exec

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestUnixTreeReparentHelper(t *testing.T) {
	if os.Getenv("XMESH_REPARENT_HELPER") != "1" {
		return
	}
	if os.Getenv("XMESH_REPARENT_CHILD") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	child := osexec.Command(os.Args[0], "-test.run=^TestUnixTreeReparentHelper$")
	child.Env = append(os.Environ(), "XMESH_REPARENT_CHILD=1")
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if os.Getenv("XMESH_REPARENT_DETACH") == "1" {
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("XMESH_REPARENT_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestUnixTreeQuickReparentingBoundary(t *testing.T) {
	for _, detach := range []bool{false, true} {
		name := "original-group"
		if detach {
			name = "detached-requires-supervisor"
		}
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "pid")
			cmd := osexec.Command(os.Args[0], "-test.run=^TestUnixTreeReparentHelper$")
			cmd.Env = append(os.Environ(), "XMESH_REPARENT_HELPER=1", "XMESH_REPARENT_PID="+file)
			if detach {
				cmd.Env = append(cmd.Env, "XMESH_REPARENT_DETACH=1")
			}
			tree, err := StartProcessTree(cmd)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tree.Close() })
			if err := tree.Wait(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := treeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			birth := ""
			for _, p := range snapshot {
				if p.pid == pid {
					birth = p.birth
				}
			}
			if birth == "" {
				t.Fatal("helper exited unexpectedly")
			}
			alive := func() bool {
				all, err := treeSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range all {
					if p.pid == pid && p.birth == birth {
						return true
					}
				}
				return false
			}
			// Detached fixtures are explicitly owned here, not by the sampled
			// tree. Preserve unrelated processes if a PID is reused.
			t.Cleanup(func() {
				if alive() {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			if err := tree.Close(); err != nil {
				t.Fatal(err)
			}
			if !detach && alive() {
				t.Fatal("reparented original-group descendant survived cleanup")
			}
			if detach && alive() {
				// An independent supervisor is required for a child that both
				// detaches and loses its parent before it can be observed.
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for alive() && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if alive() {
					t.Fatal("explicit supervisor cleanup did not finish")
				}
			}
		})
	}
}
