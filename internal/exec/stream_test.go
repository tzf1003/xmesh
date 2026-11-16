//go:build !windows

package exec

import (
	"context"
	"io"
	"os"
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
