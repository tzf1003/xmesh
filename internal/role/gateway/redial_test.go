package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

func Test_RedialDelay(t *testing.T) {
	for failures, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 4: 30 * time.Second, 1000: 30 * time.Second} {
		if got := redialDelay(failures); got != want {
			t.Fatalf("redialDelay(%d) = %s, want %s", failures, got, want)
		}
	}
}

func Test_PacedWaitsAfterFailures(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	failing := errors.New("对端尚未配对")
	calls := 0
	succeed := false
	dial := Paced(func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error) {
		calls++
		if succeed {
			return nil, nil
		}
		return nil, failing
	}, func() time.Time { return clock })
	peer := mesh.Peer{ID: "peer-1"}
	try := func(want error, wantCalls int) {
		t.Helper()
		_, err := dial(context.Background(), peer, Principal{}, mcpmux.Options{})
		if err != want || calls != wantCalls {
			t.Fatalf("at %s: err = %v, calls = %d; want err %v, calls %d", clock.Sub(time.Unix(1_700_000_000, 0)), err, calls, want, wantCalls)
		}
	}
	try(failing, 1)
	// Within the first 5 s the peer is not dialled again; the last error comes back.
	clock = clock.Add(4 * time.Second)
	try(failing, 1)
	clock = clock.Add(time.Second)
	try(failing, 2)
	// The second failure waits 10 s.
	clock = clock.Add(9 * time.Second)
	try(failing, 2)
	clock = clock.Add(time.Second)
	try(failing, 3)
	// A success clears the waiting, so the next dial goes out at once.
	clock = clock.Add(20 * time.Second)
	succeed = true
	try(nil, 4)
	succeed = false
	try(failing, 5)
	// After the success the wait starts from the first step again.
	clock = clock.Add(4 * time.Second)
	try(failing, 5)
	clock = clock.Add(time.Second)
	try(failing, 6)
}

func Test_PacedIgnoresCancelledDials(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	failing := errors.New("拨号失败")
	calls := 0
	dial := Paced(func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error) {
		calls++
		return nil, failing
	}, func() time.Time { return clock })
	peer := mesh.Peer{ID: "peer-2"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dial(cancelled, peer, Principal{}, mcpmux.Options{}); err != failing || calls != 1 {
		t.Fatalf("cancelled dial: err = %v, calls = %d", err, calls)
	}
	// The cancelled dial left no wait behind, so a live dial right after it still goes out.
	if _, err := dial(context.Background(), peer, Principal{}, mcpmux.Options{}); err != failing || calls != 2 {
		t.Fatalf("live dial after cancel: err = %v, calls = %d", err, calls)
	}
}
