package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// conformance: INV-10 the event encoder refuses command output and other content
func Test_INV10_EventEncoderRejectsOutput(t *testing.T) {
	bus := NewBus()
	for _, data := range []map[string]any{
		{"tool": "exec.run", "status": "ok", "stdout": "secret"},
		{"tool": "exec.run", "stderr": "secret"},
		{"tool": "pty.write", "data": "rm -rf /"},
		{"tool": "fs.read", "content": "file body"},
		{"tool": "exec.run", "env": map[string]any{"TOKEN": "x"}},
		{"tool": "exec.run", "args": []string{"--password", "x"}},
	} {
		if err := bus.Publish("call", data); err == nil {
			t.Errorf("accepted %v", data)
		}
	}
	if err := bus.Publish("session", map[string]any{"session_id": "p1", "data": "typed text"}); err == nil {
		t.Error("session event accepted data")
	}
	if err := bus.Publish("nope", map[string]any{}); err == nil {
		t.Error("unknown event type accepted")
	}
	if err := bus.Publish("call", map[string]any{"tool": 5}); err == nil {
		t.Error("wrong field type accepted")
	}
	if past, _, _ := bus.Subscribe(0, true); len(past) != 0 {
		t.Fatalf("rejected events were stored: %v", past)
	}
	if bus.Rejected() != 9 {
		t.Fatalf("rejected counter: %d", bus.Rejected())
	}
	if err := bus.Publish("call", map[string]any{"tool": "exec.run", "status": "ok", "duration_ms": 12, "error": nil}); err != nil {
		t.Fatal(err)
	}
}

// An audit record the logger observed can only reach the stream through the allow-list.
func Test_ControlObserveFiltersRecord(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	f.server.Observe(map[string]any{"event": "tool_call", "caller": "n/s", "device": "ctl-node", "tool": "exec.run", "status": "error", "error": "timeout", "duration_ms": 31.0, "args_digest": map[string]any{"keys": []any{"cmd"}, "cmd": "git"}, "output_sha256": strings.Repeat("a", 64), "stdout": "leak"})
	e := events.next(t, ofType("call"))
	raw, _ := json.Marshal(e)
	for _, banned := range []string{"leak", "args_digest", "git", "output_sha256"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("%q reached the stream: %s", banned, raw)
		}
	}
	if e.Data["tool"] != "exec.run" || e.Data["error"] != "timeout" || e.Data["duration_ms"] != 31.0 {
		t.Fatal(e)
	}
}

func Test_ControlEventsReplayAndGap(t *testing.T) {
	f := newFixture(t)
	bus := f.server.Bus()
	for i := 0; i < 5; i++ {
		if err := bus.Publish("warning", map[string]any{"message": "w", "active": true}); err != nil {
			t.Fatal(err)
		}
	}
	// started is event 1, the five warnings are 2..6. Resume after 4.
	s := f.stream("4")
	var ids []uint64
	for len(ids) < 2 {
		e := s.next(t, func(e Event) bool { return e.Type == "warning" })
		ids = append(ids, e.ID)
	}
	if ids[0] != 5 || ids[1] != 6 {
		t.Fatalf("replayed %v, want [5 6]", ids)
	}
	// live events follow the replay without duplication
	if err := bus.Publish("warning", map[string]any{"message": "later", "active": false}); err != nil {
		t.Fatal(err)
	}
	if e := s.next(t, func(e Event) bool { return e.Data["message"] == "later" }); e.ID != 7 {
		t.Fatalf("live id %d", e.ID)
	}
	if r := f.do("GET", "/v1/events?last_event_id=zz", nil, nil); r.Status != 400 {
		t.Fatalf("bad Last-Event-ID: %d", r.Status)
	}
	// overflow the ring, then ask for an id that fell out of it
	for i := 0; i < ringSize+10; i++ {
		_ = bus.Publish("warning", map[string]any{"message": "w", "active": true})
	}
	g := f.stream("2")
	if e := g.next(t, ofType("lifecycle")); e.Data["state"] != "gap" {
		t.Fatalf("expected gap, got %v", e)
	}
}

func Test_ControlSlowSubscriberIsDropped(t *testing.T) {
	bus := NewBus()
	_, _, sub := bus.Subscribe(0, false)
	for i := 0; i < 300; i++ {
		_ = bus.Publish("warning", map[string]any{"message": "w", "active": true})
	}
	deadline := time.After(time.Second)
	for {
		select {
		case _, open := <-sub.C:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("slow subscriber not dropped")
		}
	}
}

// Polling turns state changes into events.
func Test_ControlPolledEvents(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	f.host.mutate(func(h *fakeHost) {
		h.warnings = []string{"磁盘将满"}
		h.ptys = []map[string]any{{"id": "p-1", "name": "build", "owner": "n1/s", "state": "active", "idle_s": 1}}
		h.peers = []Peer{{ID: "n1", Name: "box", Online: true}}
	})
	if e := events.next(t, func(e Event) bool { return e.Type == "status" && e.Data["health"] == "degraded" }); e.Data["draining"] != false {
		t.Fatal(e)
	}
	if e := events.next(t, ofType("warning")); e.Data["message"] != "磁盘将满" || e.Data["active"] != true {
		t.Fatal(e)
	}
	if e := events.next(t, ofType("session")); e.Data["change"] != "opened" || e.Data["session_id"] != "p-1" || e.Data["name"] != "build" {
		t.Fatal(e)
	}
	// idle_s churn must not produce events; a state change must.
	f.host.mutate(func(h *fakeHost) { h.ptys[0]["idle_s"] = 99; h.ptys[0]["state"] = "orphaned"; h.ptys[0]["owner"] = "" })
	if e := events.next(t, ofType("session")); e.Data["change"] != "updated" || e.Data["state"] != "orphaned" {
		t.Fatal(e)
	}
	f.host.mutate(func(h *fakeHost) { h.ptys = nil; h.warnings = nil })
	if e := events.next(t, ofType("warning")); e.Data["active"] != false {
		t.Fatal(e)
	}
	if e := events.next(t, ofType("session")); e.Data["change"] != "released" {
		t.Fatal(e)
	}
}

func Test_ControlPeerEvents(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Poll = 5 * time.Millisecond })
	events := f.stream("")
	f.host.mutate(func(h *fakeHost) { h.peers = []Peer{{ID: "n1", Name: "box", Online: true}} })
	if e := events.next(t, ofType("peer")); e.Data["change"] != "joined" || e.Data["name"] != "box" {
		t.Fatal(e)
	}
	f.host.mutate(func(h *fakeHost) { h.peers[0].Online = false })
	if e := events.next(t, ofType("peer")); e.Data["change"] != "updated" || e.Data["online"] != false {
		t.Fatal(e)
	}
	f.host.mutate(func(h *fakeHost) { h.peers = nil })
	if e := events.next(t, ofType("peer")); e.Data["change"] != "left" {
		t.Fatal(e)
	}
}
