package connectivity

import (
	"context"
	"errors"
	"github.com/tzf1003/xmesh/internal/device"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDrainSeparatesConnectBudgetFromResponseBudget(t *testing.T) {
	for _, kind := range []device.PathKind{device.LAN, device.Tailnet} {
		transport := &http.Transport{ResponseHeaderTimeout: 5 * time.Second}
		observed := time.Duration(0)
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			deadline, _ := ctx.Deadline()
			observed = time.Until(deadline)
			return nil, errors.New("simulated offline path")
		}
		configureDrainTransport(transport, kind)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		_, err := transport.DialContext(ctx, "tcp", "127.0.0.1:1")
		cancel()
		expected := 5 * time.Second
		if kind == device.LAN {
			expected = 2 * time.Second
		}
		if err == nil || observed > expected || observed < expected-time.Second || transport.ResponseHeaderTimeout != 0 {
			t.Fatal(kind, observed, transport.ResponseHeaderTimeout, err)
		}
	}
}
