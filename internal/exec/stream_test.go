//go:build !windows

package exec

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/policy"
)

// A command that leaves a child in its process group, the child holding the output open, has the group terminated
// once the command exits. The output then reaches end of file without waiting for the child.
func Test_RunStreamTerminatesTheGroupWhenTheCommandExits(t *testing.T) {
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	code, timedOut, err := RunStream(context.Background(), p, t.TempDir(), os.Environ(), []string{"sh", "-c", "sleep 30 & echo started"}, "", w, w, nil)
	_ = w.Close()
	if err != nil || code != 0 || timedOut {
		t.Fatalf("RunStream = code %d, timedOut %v, err %v; want 0, false, nil", code, timedOut, err)
	}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("a child left by the command still holds its output after the command exited")
	}
}

// A context that is already done when the call begins means the command never runs: started is not called, the result
// reports timedOut, and the command's side effect does not happen.
func Test_RunStreamDoesNotStartACommandThatIsAlreadyCancelled(t *testing.T) {
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	started := false
	_, timedOut, err := RunStream(ctx, p, dir, os.Environ(), []string{"sh", "-c", `touch "$1"`, "sh", marker}, "", w, w, func() { started = true })
	_ = w.Close()
	if err != nil || !timedOut || started {
		t.Fatalf("RunStream = timedOut %v, err %v, started %v; want true, nil, false", timedOut, err, started)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran although its context was done before the start")
	}
}

// A child that holds the stdin pipe without reading it does not keep RunStream waiting. Once the command has exited,
// the stdin copy is bounded, so the sweep terminates the child and the call returns. The 20 second context is only a
// backstop: without the bound, the call would end only when that context did, and timedOut would be set.
func Test_RunStreamDoesNotWaitForAChildThatHoldsStdin(t *testing.T) {
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// The payload is larger than a pipe buffer, so the copy into stdin blocks while the child holds the pipe. The child
	// keeps the pipe through fd 3, because a background command otherwise reads from /dev/null.
	payload := strings.Repeat("p", 1<<20)
	code, timedOut, err := RunStream(ctx, p, t.TempDir(), os.Environ(), []string{"sh", "-c", "exec 3<&0; sleep 30 <&3 & echo started"}, payload, w, w, nil)
	_ = w.Close()
	if err != nil || code != 0 || timedOut {
		t.Fatalf("RunStream = code %d, timedOut %v, err %v; want 0, false, nil", code, timedOut, err)
	}
}
