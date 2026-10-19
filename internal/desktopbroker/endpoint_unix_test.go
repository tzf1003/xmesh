//go:build darwin || linux

package desktopbroker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/control"
)

func TestLongStatePathResolvesTheBoundDesktopEndpoint(t *testing.T) {
	state := filepath.Join(t.TempDir(), strings.Repeat("long-state-", 12))
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	// Bind a unique sibling so the test preserves any installed desktop host.
	listener, bound, err := control.Listen(control.ListenOptions{StateDir: state, Node: fmt.Sprintf("desktop-endpoint-%d", os.Getpid())})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	endpoint := computer.EndpointForState(state)
	want := filepath.Join(filepath.Dir(bound.Address), "computer.sock")
	if endpoint != want || strings.HasPrefix(endpoint, state) {
		t.Fatal("client and listener disagree on the fallback", endpoint, bound.Address)
	}
}
