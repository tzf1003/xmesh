package filelock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessLockHelper(t *testing.T) {
	path := os.Getenv("XMESH_FILELOCK_HELPER")
	if path == "" {
		return
	}
	f, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestProcessLockCancellationAndCrashRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLockHelper$")
	cmd.Env = append(os.Environ(), "XMESH_FILELOCK_HELPER="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "locked\n" && line != "locked\r\n" {
			t.Fatalf("helper readiness: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock holder did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if f, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if f != nil {
			f.Close()
		}
		t.Fatal("another process bypassed held lock", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, err := Acquire(ctx, path)
	if err != nil {
		t.Fatal("process death retained lock", err)
	}
	defer f.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("persistent lock file missing", err)
	}
}
