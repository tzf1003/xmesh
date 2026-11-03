package computer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// withCapabilities changes what the fake desktop reports on its next Info.
func withCapabilities(f *fakeDesktop, caps ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info.Capabilities = caps
}

func TestToolListedFollowsTheLastReportedCapabilities(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	m := New(f, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	if !m.ToolListed("computer.snapshot") {
		t.Fatal("before any report every tool stays listed")
	}
	if _, err := m.Info(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"computer.snapshot", "computer.ref_act"} {
		if m.ToolListed(tool) {
			t.Fatalf("%s is listed without the refs capability", tool)
		}
	}
	for _, tool := range []string{"computer.info", "computer.observe", "computer.act", "computer.open"} {
		if !m.ToolListed(tool) {
			t.Fatalf("%s is hidden though it needs no capability", tool)
		}
	}
	withCapabilities(f, "screenshot", "pointer", "keyboard", "text", "refs")
	if _, err := m.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.ToolListed("computer.snapshot") || !m.ToolListed("computer.ref_act") {
		t.Fatal("refs tools stay hidden after the engine reports refs")
	}
}

func TestOnCapabilitiesFiresOnlyWhenTheSetChanges(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	m := New(f, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	fired := make(chan struct{}, 8)
	m.OnCapabilities(func() { fired <- struct{}{} })
	expect := func(want bool) {
		t.Helper()
		select {
		case <-fired:
			if !want {
				t.Fatal("the hook fired without a change in capabilities")
			}
		case <-time.After(300 * time.Millisecond):
			if want {
				t.Fatal("the hook did not fire for a change in capabilities")
			}
		}
	}
	if _, err := m.Info(ctx); err != nil {
		t.Fatal(err)
	}
	expect(true) // the first successful report establishes the set
	if _, err := m.Info(ctx); err != nil {
		t.Fatal(err)
	}
	expect(false)
	withCapabilities(f, "screenshot", "pointer", "keyboard", "text", "refs")
	if _, err := m.Info(ctx); err != nil {
		t.Fatal(err)
	}
	expect(true)
}

// flakyDesktop fails Info while down is set, as an unreachable executor does.
type flakyDesktop struct {
	*fakeDesktop
	down atomic.Bool
}

func (f *flakyDesktop) Info(ctx context.Context) (Info, error) {
	if f.down.Load() {
		return Info{}, errors.New("executor disconnected")
	}
	return f.fakeDesktop.Info(ctx)
}

// With no caller at all, the sweep learns the capabilities once the executor is reachable, and the hook
// rebuilds the tool lists. Failed attempts do not count as a report.
func TestCapabilitiesAreLearnedWithoutAnyCaller(t *testing.T) {
	f := &flakyDesktop{fakeDesktop: newFake()}
	f.down.Store(true)
	m := New(f, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	m.mu.Lock()
	m.probeEvery = 0
	m.mu.Unlock()
	fired := make(chan struct{}, 8)
	m.OnCapabilities(func() { fired <- struct{}{} })
	time.Sleep(1500 * time.Millisecond)
	if _, known := m.Capabilities(); known {
		t.Fatal("a failed probe was counted as a report")
	}
	select {
	case <-fired:
		t.Fatal("the hook fired without a report")
	default:
	}
	f.down.Store(false)
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("capabilities were not learned once the executor became reachable")
	}
	if _, known := m.Capabilities(); !known || m.ToolListed("computer.snapshot") {
		t.Fatal("refs tools listed for an engine without refs")
	}
}
