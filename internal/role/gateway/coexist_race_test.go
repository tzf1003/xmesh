package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

func TestPacedIgnoresFailureFromBeforeSuccessfulDial(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	failure := errors.New("old path failed")
	dial := Paced(func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return nil, failure
		}
		return nil, nil
	}, time.Now)
	peer := mesh.Peer{ID: "device", Address: "https://192.168.1.2:7433/mcp"}
	done := make(chan error, 1)
	go func() { _, err := dial(context.Background(), peer, Principal{}, mcpmux.Options{}); done <- err }()
	<-started
	if _, err := dial(context.Background(), peer, Principal{}, mcpmux.Options{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != failure {
		t.Fatal(err)
	}
	if _, err := dial(context.Background(), peer, Principal{}, mcpmux.Options{}); err != nil {
		t.Fatalf("successful path inherited stale failure: %v", err)
	}
}

func TestPacedKeepsPathsIndependent(t *testing.T) {
	failure := errors.New("LAN unavailable")
	dial := Paced(func(_ context.Context, peer mesh.Peer, _ Principal, _ mcpmux.Options) (*mcpmux.Bridge, error) {
		if peer.Address == "lan" {
			return nil, failure
		}
		return nil, nil
	}, time.Now)
	if _, err := dial(context.Background(), mesh.Peer{ID: "device", Address: "lan"}, Principal{}, mcpmux.Options{}); err != failure {
		t.Fatal(err)
	}
	if _, err := dial(context.Background(), mesh.Peer{ID: "device", Address: "tailnet"}, Principal{}, mcpmux.Options{}); err != nil {
		t.Fatalf("Tailnet inherited LAN backoff: %v", err)
	}
}

func TestDeviceInfoDiscardsReadingFromReplacedPath(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	g := New(Options{Dial: func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error) {
		close(started)
		<-release
		return nil, errors.New("old LAN endpoint unavailable")
	}})
	t.Cleanup(g.Close)
	g.Update([]mesh.Peer{{ID: "device", Name: "workstation", Address: "lan", Online: true}})
	g.pollInfo(time.Now())
	<-started
	g.Update([]mesh.Peer{{ID: "device", Name: "workstation", Address: "tailnet", Online: true}})
	close(release)
	g.infoWorkers.Wait()
	if info := g.DeviceInfo()["workstation"]; info.Error != "" || !info.RefreshedAt.IsZero() {
		t.Fatalf("stale path read published: %+v", info)
	}
}

func TestDeviceInfoOfflineCompletionDoesNotBlockLaterReads(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	g := New(Options{Dial: func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil, errors.New("unreachable")
	}})
	t.Cleanup(g.Close)
	peer := mesh.Peer{ID: "device", Name: "workstation", Address: "lan", Online: true}
	g.Update([]mesh.Peer{peer})
	g.pollInfo(time.Now())
	<-started
	g.Update(nil)
	close(release)
	g.infoWorkers.Wait()
	g.Update([]mesh.Peer{peer})
	g.pollInfo(time.Now())
	g.infoWorkers.Wait()
	if calls.Load() != 2 {
		t.Fatal("completed offline read blocked recovery", calls.Load())
	}
}
