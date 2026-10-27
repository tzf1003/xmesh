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

func TestDirectoryReportsPathQualityAcrossFlaps(t *testing.T) {
	d, i, generation := directory(t)
	address := "100.100.1.1:7433"
	start := time.Now()
	ok := func(at time.Time, rtt time.Duration) {
		t.Helper()
		path := observedPath(Tailnet, address, "boot-a")
		path.VerifiedAt, path.ExpiresAt, path.RTT = at, at.Add(30*time.Second), rtt
		if err := d.Observe(i.Certificate.Leaf, generation, path); err != nil {
			t.Fatal(err)
		}
	}
	ok(start, 80*time.Millisecond)
	if err := d.ProbeFailed(i.ID, Tailnet, address, 1, start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ok(start.Add(2*time.Second), 120*time.Millisecond)
	devices, _ := d.Snapshot(start.Add(3 * time.Second))
	q := devices[0].Paths[0].Quality
	if q.Samples != 3 || q.Flaps != 1 || q.Loss < 0.33 || q.Loss > 0.34 || q.LatencyMs != 100 {
		t.Fatalf("quality = %+v", q)
	}
	// Losing the provider resets the path's history with the path itself.
	if err := d.SetProvider(Tailnet, 2, true); err != nil {
		t.Fatal(err)
	}
	devices, _ = d.Snapshot(start.Add(4 * time.Second))
	if len(devices[0].Paths) != 0 {
		t.Fatalf("paths kept: %+v", devices[0].Paths)
	}
	ok2 := observedPath(Tailnet, address, "boot-a")
	ok2.ProviderGeneration = 2
	if err := d.Observe(i.Certificate.Leaf, generation, ok2); err != nil {
		t.Fatal(err)
	}
	devices, _ = d.Snapshot(time.Now())
	if q := devices[0].Paths[0].Quality; q.Samples != 1 || q.Flaps != 0 {
		t.Fatalf("history survived provider reset: %+v", q)
	}
}

func TestDirectoryReportsPathThatNeverAnswered(t *testing.T) {
	d, i, _ := directory(t)
	address := "100.100.1.1:7433"
	now := time.Now()
	for n := range 3 {
		if err := d.ProbeFailed(i.ID, Tailnet, address, 1, now.Add(time.Duration(n)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	devices, _ := d.Snapshot(now.Add(4 * time.Second))
	if len(devices) != 1 || len(devices[0].Paths) != 1 {
		t.Fatalf("blocked path hidden: %+v", devices)
	}
	path := devices[0].Paths[0]
	if path.Reachable || path.Kind != Tailnet || path.Address != address || path.Quality.Samples != 3 || path.Quality.Loss != 1 {
		t.Fatalf("path = %+v", path)
	}
	// Once its probes age out of the window the path is gone again.
	devices, _ = d.Snapshot(now.Add(statsWindow + time.Minute))
	if len(devices[0].Paths) != 0 {
		t.Fatalf("stale failures kept: %+v", devices[0].Paths)
	}
}

func TestDirectoryKeepsRecordingWhenManyAddressesWereSeen(t *testing.T) {
	d, i, generation := directory(t)
	start := time.Now()
	for n := range maxStatsPerDevice + 10 {
		path := observedPath(LAN, "192.168.1."+string(rune('A'+n%26))+string(rune('a'+n/26))+":7433", "boot-a")
		path.VerifiedAt = start.Add(time.Duration(n) * time.Second)
		path.ExpiresAt = path.VerifiedAt.Add(time.Minute)
		if err := d.Observe(i.Certificate.Leaf, generation, path); err != nil {
			t.Fatal(err)
		}
	}
	devices, _ := d.Snapshot(start.Add(time.Duration(maxStatsPerDevice+10) * time.Second))
	newest := "192.168.1." + string(rune('A'+(maxStatsPerDevice+9)%26)) + string(rune('a'+(maxStatsPerDevice+9)/26)) + ":7433"
	for _, path := range devices[0].Paths {
		if path.Address == newest {
			if path.Quality.Samples != 1 {
				t.Fatalf("newest path lost its quality: %+v", path)
			}
			return
		}
	}
	t.Fatal("newest path missing")
}

func TestRevokedDeviceForgetsPathThatNeverAnswered(t *testing.T) {
	d, i, _ := directory(t)
	now := time.Now()
	if err := d.ProbeFailed(i.ID, Tailnet, "100.100.1.1:7433", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := d.trust.Revoke(t.Context(), i.ID); err != nil {
		t.Fatal(err)
	}
	// The revocation is applied by the directory's own goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.mu.Lock()
		_, kept := d.stats[i.ID]
		d.mu.Unlock()
		if !kept {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked device kept its probe history")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDirectoryRecordsHeartbeatOnlyOnKnownPathAndKeepsItAlive(t *testing.T) {
	d, i, generation := directory(t)
	address := "192.168.1.4:7433"
	start := time.Now()
	if err := d.RecordLink(i.ID, LAN, address, 1, start, true, time.Millisecond); !errors.Is(err, ErrStalePath) {
		t.Fatalf("heartbeat introduced a path: %v", err)
	}
	path := observedPath(LAN, address, "boot-a")
	path.VerifiedAt, path.ExpiresAt, path.RTT = start, start.Add(30*time.Second), 40*time.Millisecond
	if err := d.Observe(i.Certificate.Leaf, generation, path); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		if err := d.RecordLink(i.ID, LAN, address, 1, start.Add(time.Duration(n)*10*time.Second), true, 10*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	// No probe since the first one, yet the path is still reachable 45s later and the heartbeats count.
	devices, _ := d.Snapshot(start.Add(45 * time.Second))
	got := devices[0].Paths[0]
	if !got.Reachable || got.Quality.Samples != 5 || got.Quality.LatencyMs != 10 || got.Direction != DirectionOutgoing {
		t.Fatalf("path = %+v", got)
	}
	if err := d.RecordLink(i.ID, LAN, address, 1, start.Add(50*time.Second), false, 0); err != nil {
		t.Fatal(err)
	}
	devices, _ = d.Snapshot(start.Add(51 * time.Second))
	if q := devices[0].Paths[0].Quality; q.Samples != 6 || q.Flaps != 1 {
		t.Fatalf("failed heartbeat not recorded: %+v", q)
	}
	if err := d.RecordLink(i.ID, LAN, address, 2, start, true, 0); !errors.Is(err, ErrStalePath) {
		t.Fatalf("stale generation accepted: %v", err)
	}
}

func TestDirectoryPeerQualityIsBoundedAndExpires(t *testing.T) {
	d, i, generation := directory(t)
	if err := d.Observe(i.Certificate.Leaf, generation, observedPath(LAN, "192.168.1.4:7433", "boot-a")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	good := Quality{Samples: 20, LatencyMs: 12, P90Ms: 15, JitterMs: 3, Loss: 0.05, Flaps: 1}
	d.SetPeerQuality(i.ID, now, []KindQuality{
		{Kind: LAN, Quality: good},
		{Kind: LAN, Quality: Quality{Samples: 1}},              // duplicate kind
		{Kind: "bogus", Quality: good},                         // unknown kind
		{Kind: Tailnet, Quality: Quality{Samples: 1, Loss: 2}}, // impossible loss
	})
	devices, _ := d.Snapshot(now.Add(time.Second))
	if got := devices[0].PeerQuality; len(got) != 1 || got[0].Kind != LAN || got[0].Quality != good || !got[0].ReceivedAt.Equal(now) {
		t.Fatalf("peer quality = %+v", got)
	}
	devices, _ = d.Snapshot(now.Add(PeerQualityTTL + time.Second))
	if len(devices[0].PeerQuality) != 0 {
		t.Fatalf("stale report shown: %+v", devices[0].PeerQuality)
	}
	// What this end reports is the best-measured address of each kind.
	local := d.LocalQuality(i.ID, now)
	if len(local) != 1 || local[0].Kind != LAN || local[0].Quality.Samples != 1 {
		t.Fatalf("local quality = %+v", local)
	}
}
