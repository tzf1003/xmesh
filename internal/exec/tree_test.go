package exec

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("XMESH_TREE_HELPER") != "1" {
		return
	}
	if os.Getenv("XMESH_TREE_CHILD") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	child := osexec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
	child.Env = append(os.Environ(), "XMESH_TREE_CHILD=1")
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("XMESH_TREE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		os.Exit(3)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestProcessTreeClosesDescendants(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pid")
	cmd := osexec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
	cmd.Env = append(os.Environ(), "XMESH_TREE_HELPER=1", "XMESH_TREE_PID="+file)
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = out
	tree, err := StartProcessTree(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(file); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	_ = tree.Wait()
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
}
