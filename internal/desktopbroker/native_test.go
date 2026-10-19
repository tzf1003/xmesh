package desktopbroker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
)

// This opt-in test exercises the real native binary through the protected local
// listener. Capture is a separate opt-in and never synthesizes user input.
func TestNativeDesktopRoundTrip(t *testing.T) {
	helper := os.Getenv("XMESH_TEST_COMPUTER_HELPER")
	if helper == "" {
		t.Skip("set XMESH_TEST_COMPUTER_HELPER to a built native executor")
	}
	b, err := Start(Options{StateDir: t.TempDir(), HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := computer.NewClient(b.Endpoint)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := c.Info(ctx)
	if err != nil || info.ProtocolVersion != 1 || info.Epoch == "" {
		t.Fatal(info, err)
	}
	if os.Getenv("XMESH_TEST_COMPUTER_CAPTURE") != "1" {
		return
	}
	for n := 0; n < 10 && !info.Available; n++ {
		time.Sleep(100 * time.Millisecond)
		info, err = c.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !info.Available {
		t.Fatal("capture requested but desktop unavailable", info.State, info.Reason)
	}
	m := computer.New(c, computer.Settings{Enabled: true})
	defer m.Close()
	s, err := m.Open(ctx, "native-test/session", computer.OpenRequest{Mode: "observe"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	frame, err := m.Observe(ctx, "native-test/session", computer.ObserveRequest{SessionRequest: computer.SessionRequest{SessionID: s.ID, Epoch: s.Epoch}, CaptureRequest: computer.CaptureRequest{MaxWidth: 1280}})
	if err != nil || frame.Width < 1 || frame.Height < 1 || frame.Width > 1280 || len(frame.Image) == 0 || frame.Epoch != info.Epoch {
		t.Fatal(frame.ID, frame.Width, frame.Height, err)
	}
	t.Logf("native screenshot: %dx%d, %d bytes, %s", frame.Width, frame.Height, len(frame.Image), time.Since(start).Round(time.Millisecond))
}
