package device

import (
	"errors"
	"testing"
	"time"
)

func directory(t *testing.T) (*Directory, *Identity, uint64) {
	t.Helper()
	s := trustStore(t)
	i := identity(t)
	if err := s.Grant(t.Context(), i.Document("workstation"), []string{"tag:peer", "tag:device"}, nil); err != nil {
		t.Fatal(err)
	}
	trust, _ := s.Find(i.ID)
	d := NewDirectory(t.Context(), s, "local")
	t.Cleanup(d.Close)
	if err := d.SetProvider(LAN, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetProvider(Tailnet, 1, true); err != nil {
		t.Fatal(err)
	}
	return d, i, trust.Generation
}

func observedPath(kind PathKind, endpoint, boot string) Path {
	now := time.Now()
	return Path{Kind: kind, Address: endpoint, BootID: boot, ProviderGeneration: 1, VerifiedAt: now, ExpiresAt: now.Add(30 * time.Second)}
}

func TestDirectoryOneDeviceAcrossPathsAndIndependentLoss(t *testing.T) {
	d, i, generation := directory(t)
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(Tailnet, "100.100.1.1:7433", "boot-a")); err != nil {
		t.Fatal(err)
	}
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(LAN, "192.168.1.4:7433", "boot-a")); err != nil {
		t.Fatal(err)
	}
	devices, _ := d.Snapshot(time.Now())
	if len(devices) != 1 || devices[0].DeviceID != i.ID || len(devices[0].Paths) != 2 || devices[0].Paths[0].Kind != LAN || devices[0].IdentityConflict {
		t.Fatalf("device merge: %+v", devices)
	}
	if err := d.Lost(i.ID, LAN, "192.168.1.4:7433", 1); err != nil {
		t.Fatal(err)
	}
	devices, _ = d.Snapshot(time.Now())
	if len(devices) != 1 || devices[0].Paths[0].Reachable || !devices[0].Paths[1].Reachable {
		t.Fatalf("one path loss removed device: %+v", devices)
	}
}

func TestDirectoryStoppedProviderRejectsLateProbeAndGoodbye(t *testing.T) {
	d, i, generation := directory(t)
	path := observedPath(LAN, "192.168.1.4:7433", "boot-a")
	if err := d.Observe(i.Certificate.Leaf, generation, path); err != nil {
		t.Fatal(err)
	}
	if err := d.SetProvider(LAN, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := d.Observe(i.Certificate.Leaf, generation, path); !errors.Is(err, ErrStalePath) {
		t.Fatal("late probe accepted", err)
	}
	path.ProviderGeneration = 2
	if err := d.Observe(i.Certificate.Leaf, generation, path); err != nil {
		t.Fatal(err)
	}
	if err := d.Lost(i.ID, LAN, path.Address, 1); !errors.Is(err, ErrStalePath) {
		t.Fatal("late goodbye accepted", err)
	}
	devices, _ := d.Snapshot(time.Now())
	if len(devices[0].Paths) != 1 || !devices[0].Paths[0].Reachable {
		t.Fatal(devices)
	}
}

func TestDirectoryRejectsUntrustedAndDetectsClonedIdentity(t *testing.T) {
	d, i, generation := directory(t)
	if err := d.Observe(identity(t).Certificate.Leaf, generation, observedPath(LAN, "lan", "boot-x")); !errors.Is(err, ErrUntrusted) {
		t.Fatal(err)
	}
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(LAN, "lan", "boot-a")); err != nil {
		t.Fatal(err)
	}
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(Tailnet, "tailnet", "boot-b")); err != nil {
		t.Fatal(err)
	}
	devices, _ := d.Snapshot(time.Now())
	if !devices[0].IdentityConflict {
		t.Fatal("simultaneous process identities merged as healthy")
	}
	if err := d.trust.Revoke(t.Context(), i.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(LAN, "lan", "boot-a")); !errors.Is(err, ErrUntrusted) {
		t.Fatal(err)
	}
	if devices, _ := d.Snapshot(time.Now()); len(devices) != 0 {
		t.Fatal("revoked device remained visible", devices)
	}
}

func TestDirectoryNotifiesEverySubscriber(t *testing.T) {
	d, i, generation := directory(t)
	a, stopA := d.Subscribe()
	defer stopA()
	b, stopB := d.Subscribe()
	defer stopB()
	<-a
	<-b
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(LAN, "lan", "boot-a")); err != nil {
		t.Fatal(err)
	}
	_, version := d.Snapshot(time.Now())
	for _, changed := range []<-chan uint64{a, b} {
		select {
		case got := <-changed:
			if got != version {
				t.Fatal("unexpected version", got, version)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber missed update")
		}
	}
}

func TestFailedProbeCannotRetireNewerEvidence(t *testing.T) {
	d, id, generation := directory(t)
	path := observedPath(LAN, "https://192.168.1.2:7433", "boot-new")
	oldProbe := path.VerifiedAt.Add(-time.Second)
	if err := d.Observe(id.Certificate.Leaf, generation, path); err != nil {
		t.Fatal(err)
	}
	if err := d.ProbeFailed(id.ID, LAN, path.Address, 1, oldProbe); !errors.Is(err, ErrStalePath) {
		t.Fatal("old failure overwrote current evidence", err)
	}
	devices, _ := d.Snapshot(time.Now())
	if !devices[0].Paths[0].Reachable {
		t.Fatal("new path retired")
	}
	if err := d.ProbeFailed(id.ID, LAN, path.Address, 1, path.VerifiedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	devices, _ = d.Snapshot(time.Now())
	if devices[0].Paths[0].Reachable {
		t.Fatal("failed endpoint stayed reachable")
	}
}

func TestFailedOldProcessEvidenceClearsIdentityConflict(t *testing.T) {
	d, id, generation := directory(t)
	old := observedPath(LAN, "https://192.168.1.2:7433", "old-process")
	newPath := observedPath(Tailnet, "https://100.64.0.2:7433", "new-process")
	for _, path := range []Path{old, newPath} {
		if err := d.Observe(id.Certificate.Leaf, generation, path); err != nil {
			t.Fatal(err)
		}
	}
	if !d.IdentityConflict(id.ID, time.Now()) {
		t.Fatal("simultaneous live identities were merged")
	}
	if err := d.ProbeFailed(id.ID, old.Kind, old.Address, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if d.IdentityConflict(id.ID, time.Now()) {
		t.Fatal("failed old process kept device in conflict")
	}
}
