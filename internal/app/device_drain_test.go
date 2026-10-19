package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

type deadlineDrainSender struct {
	budget chan time.Duration
}

func (s deadlineDrainSender) Peers(context.Context) ([]mesh.Peer, error) {
	return []mesh.Peer{{ID: "one", Name: "one", Online: true, Tags: []string{"tag:device"}}}, nil
}
func (s deadlineDrainSender) SendDrain(ctx context.Context, _ mesh.Peer, _ agent.DrainRequest) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("missing deadline")
	}
	s.budget <- time.Until(deadline)
	<-ctx.Done()
	return ctx.Err()
}
func TestRemoteDrainHonorsDeviceTimeout(t *testing.T) {
	sender := deadlineDrainSender{budget: make(chan time.Duration, 1)}
	outcomes, err := remoteDrain(t.Context(), sender, v2.DrainRequest{Scope: "mesh", TimeoutMS: 20})
	budget := <-sender.budget
	if err != nil || len(outcomes) != 1 || !strings.Contains(outcomes[0].Error, "deadline exceeded") || budget <= 0 || budget > 20*time.Millisecond {
		t.Fatal(outcomes, budget, err)
	}
	for _, requested := range []int64{0, 45000} {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := remoteDrain(ctx, sender, v2.DrainRequest{Scope: "mesh", TimeoutMS: requested})
			done <- err
		}()
		budget = <-sender.budget
		expected := time.Duration(requested) * time.Millisecond
		if requested == 0 {
			expected = 15 * time.Second
		}
		cancel()
		if err := <-done; err != nil || budget > expected || budget < expected-time.Second {
			t.Fatal(requested, budget, err)
		}
	}
}
