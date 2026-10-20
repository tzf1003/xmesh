package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// A peer that keeps failing waits before its next dial. The wait starts at redialFirst, doubles with
// each failure and stops at redialLimit; one success clears it. Without this, an online peer that
// refuses the connection, one that has not paired with this node say, was dialled every few seconds
// for ever, and it logged a failed handshake on every attempt.
const (
	redialFirst = 5 * time.Second
	redialLimit = 30 * time.Second
)

// redialDelay is the wait after the given number of consecutive failures.
func redialDelay(failures int) time.Duration {
	delay := redialFirst
	for i := 1; i < failures && delay < redialLimit; i++ {
		delay *= 2
	}
	return min(delay, redialLimit)
}

// dialWait is the pacing state of one peer that failed to dial.
type dialWait struct {
	failures      int
	until         time.Time
	err           error
	next, applied uint64
}

// Paced wraps dial with the wait after failures. A dial made during the wait returns the last error
// at once, without touching the network. A dial that the caller cancelled says nothing about the
// peer, so it neither counts as a failure nor clears the wait.
func Paced(dial Dial, now func() time.Time) Dial {
	var mu sync.Mutex
	type pathKey struct{ id, address string }
	waits := map[pathKey]*dialWait{}
	return func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		key := pathKey{peer.ID, peer.Address}
		mu.Lock()
		w := waits[key]
		if w == nil {
			w = &dialWait{}
			waits[key] = w
		}
		if now().Before(w.until) {
			err := w.err
			mu.Unlock()
			return nil, err
		}
		w.next++
		attempt := w.next
		mu.Unlock()
		bridge, err := dial(ctx, peer, p, opts)
		if err != nil && ctx.Err() != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if attempt < w.applied {
			return bridge, err
		}
		w.applied = attempt
		if err == nil {
			w.failures, w.until, w.err = 0, time.Time{}, nil
			return bridge, nil
		}
		w.failures++
		w.err = err
		w.until = now().Add(redialDelay(w.failures))
		return nil, err
	}
}
