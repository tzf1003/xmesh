package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// conformance: INV-10 the event encoder refuses command output and other content
func Test_INV10_EventEncoderRejectsOutput(t *testing.T) {
	bus := NewBus()
	call := func(extra map[string]any) map[string]any {
		data := map[string]any{"tool": "exec.run", "device": "d", "caller": "n/s", "oauth_client": nil, "status": "ok", "error": nil, "duration_ms": 1}
		for k, v := range extra {
			data[k] = v
		}
		return data
	}
	for _, data := range []map[string]any{
		call(map[string]any{"stdout": "secret"}),
		call(map[string]any{"stderr": "secret"}),
		call(map[string]any{"data": "rm -rf /"}),
		call(map[string]any{"content": "file body"}),
		call(map[string]any{"env": map[string]any{"TOKEN": "x"}}),
		call(map[string]any{"args": []string{"--password", "x"}}),
		call(map[string]any{"duration_ms": "slow"}),
	} {
		if err := bus.Publish(v1.EventCall, data); err == nil {
			t.Errorf("accepted %v", data)
		}
	}
	session := map[string]any{"change": "opened", "session": map[string]any{"kind": "pty", "id": "p-abcdef12", "name": nil, "owner": "n", "oauth_client": nil, "pid": 1, "state": "active", "idle_s": 0, "cols": 80, "rows": 24, "cwd": "/", "created_at": "2026-10-16T15:30:00.123Z", "data": "typed text"}}
	if err := bus.Publish(v1.EventSession, session); err == nil {
		t.Error("session event accepted terminal data")
	}
	if err := bus.Publish("nope", map[string]any{}); err == nil {
		t.Error("unknown event type accepted")
	}
	if err := bus.Publish(v1.EventHello, v1.HelloData{BootID: "x"}); err == nil {
		t.Error("hello must not enter the replay ring")
	}
	if join := bus.Subscribe(""); join.Head != nil {
		t.Fatalf("rejected events were stored: %v", *join.Head)
	}
	if bus.Rejected() != 10 {
		t.Fatalf("rejected counter: %d", bus.Rejected())
	}
	if err := bus.Publish(v1.EventCall, call(nil)); err != nil {
		t.Fatal(err)
	}
}

// An audit record the logger observed can only reach the stream through the allow-list.
func Test_ControlObserveFiltersRecord(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	f.server.Observe(map[string]any{"event": "tool_call", "caller": "n/s", "device": "ctl-node", "tool": "exec.run", "status": "error", "error": "timeout", "duration_ms": 31.0, "args_digest": map[string]any{"keys": []any{"cmd"}, "cmd": "git"}, "output_sha256": strings.Repeat("a", 64), "stdout": "leak"})
	e := events.next(t, ofType(v1.EventCall))
	for _, banned := range []string{"leak", "args_digest", "git", "output_sha256"} {
		if strings.Contains(string(e.Data), banned) {
			t.Fatalf("%q reached the stream: %s", banned, e.Data)
		}
	}
	var data v1.CallEventData
	payload(t, e, &data)
	if data.Tool != "exec.run" || *data.Error != "timeout" || data.DurationMS != 31 || data.OAuthClient != nil {
		t.Fatal(data)
	}
	// control_call records are not tool calls
	f.server.Observe(map[string]any{"event": "control_call", "caller": "control:uid-1", "device": "ctl-node", "tool": "control.drain.start", "status": "ok", "duration_ms": 1})
	f.server.Observe(map[string]any{"event": "tool_call", "caller": "n/s", "device": "ctl-node", "tool": "fs.read", "status": "ok", "duration_ms": 2})
	if e = events.next(t, ofType(v1.EventCall)); !strings.Contains(string(e.Data), "fs.read") {
		t.Fatalf("a control_call was relayed as a tool call: %s", e.Data)
	}
}

func Test_ControlEventsHelloReplayAndReset(t *testing.T) {
	f := newFixture(t)
	bus := f.server.Bus()
	boot := bus.BootID()
	warn := v1.WarningEventData{Change: "raised", Warning: v1.Warning{Code: v1.WarnQuotaExhausted, Severity: v1.SeverityWarning, Message: "w"}}
	for i := 0; i < 5; i++ {
		if err := bus.Publish(v1.EventWarning, warn); err != nil {
			t.Fatal(err)
		}
	}
	// The running lifecycle event is 1, the warnings are 2..6.
	first := f.stream("")
	hello := first.next(t, ofType(v1.EventHello))
	var h v1.HelloData
	payload(t, hello, &h)
	if hello.ID != "" || h.BootID != boot || h.HeartbeatS != v1.HeartbeatSeconds || h.Head == nil || !strings.HasPrefix(*h.Head, boot+".") {
		t.Fatalf("%+v %+v", hello, h)
	}
	resumed := f.stream(boot + ".4")
	if e := resumed.next(t, ofType(v1.EventHello)); e.Type != v1.EventHello {
		t.Fatal(e)
	}
	var seqs []uint64
	for len(seqs) < 2 {
		seqs = append(seqs, resumed.next(t, ofType(v1.EventWarning)).Seq)
	}
	if seqs[0] != 5 || seqs[1] != 6 {
		t.Fatalf("replayed %v, want [5 6]", seqs)
	}
	// live events follow the replay without duplication
	if err := bus.Publish(v1.EventWarning, v1.WarningEventData{Change: "cleared", Warning: warn.Warning}); err != nil {
		t.Fatal(err)
	}
	if e := resumed.next(t, ofType(v1.EventWarning)); e.Seq != 7 || e.ID != boot+".7" {
		t.Fatalf("live %+v", e)
	}
	// an id of another boot: reset, then the stream continues from the head
	other := f.stream("deadbeef.3")
	other.next(t, ofType(v1.EventHello))
	var reset v1.ResetData
	payload(t, other.next(t, ofType(v1.EventReset)), &reset)
	if reset.Reason != "boot_changed" || reset.Head == nil || *reset.Head != boot+".7" {
		t.Fatalf("%+v", reset)
	}
	// an id from this boot that fell out of the ring: gap
	for i := 0; i < v1.EventRingSize+10; i++ {
		_ = bus.Publish(v1.EventWarning, warn)
	}
	gap := f.stream(boot + ".2")
	gap.next(t, ofType(v1.EventHello))
	payload(t, gap.next(t, ofType(v1.EventReset)), &reset)
	if reset.Reason != "gap" {
		t.Fatalf("%+v", reset)
	}
	// garbage is treated as an unknown boot rather than rejected
	junk := f.stream("zz")
	junk.next(t, ofType(v1.EventHello))
	payload(t, junk.next(t, ofType(v1.EventReset)), &reset)
	if reset.Reason != "boot_changed" {
		t.Fatalf("%+v", reset)
	}
}

func Test_ControlEventsHeartbeat(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Heartbeat = 20 * time.Millisecond })
	ctx := t.Context()
	res, err := f.client.Stream(ctx, "/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != v1.ContentTypeSSE {
		t.Fatal(res.Header)
	}
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 512)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(buf), v1.Heartbeat) && time.Now().Before(deadline) {
		n, err := res.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(buf), v1.Heartbeat) {
		t.Fatalf("no heartbeat: %q", buf)
	}
}

func Test_ControlSlowSubscriberIsDropped(t *testing.T) {
	bus := NewBus()
	sub := bus.Subscribe("").Sub
	warn := v1.WarningEventData{Change: "raised", Warning: v1.Warning{Code: v1.WarnQuotaExhausted, Severity: v1.SeverityWarning, Message: "w"}}
	for i := 0; i < 300; i++ {
		_ = bus.Publish(v1.EventWarning, warn)
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
		h.warnings = []v1.Warning{{Code: v1.WarnQuotaExhausted, Severity: v1.SeverityWarning, Message: "配额已满"}}
	})
	// The first status event is not awaited here: the poller keys its status baseline after its
	// first peer refresh, so on a slow host a change made right after Start can already be in it.
	// The warning baseline is taken before serving, so the warning event always comes.
	var w v1.WarningEventData
	payload(t, events.next(t, ofType(v1.EventWarning)), &w)
	if w.Change != "raised" || w.Warning.Code != v1.WarnQuotaExhausted {
		t.Fatal(w)
	}
	// Status and session samples are independent; introduce sessions after consuming the
	// warning so filtering for the warning cannot discard an earlier session event.
	f.host.mutate(func(h *fakeHost) {
		h.ptys = []v1.PtySession{pty("p-abcdef12")}
		h.execs = []v1.ExecRun{{Kind: "exec", ID: "e-1", Caller: "n1/s", Cmd: "cargo", Mode: "shared", State: "queued", StartedAt: v1.Now()}}
		h.leases = []v1.BackendLease{{Kind: "lease", Resource: "backend:ida", Owner: "n1/s", Token: 1, ExpiresAt: v1.Now()}}
	})
	opened := map[string]string{}
	for len(opened) < 3 {
		var s v1.SessionEventData
		payload(t, events.next(t, ofType(v1.EventSession)), &s)
		if s.Change != "opened" {
			t.Fatalf("%+v", s)
		}
		switch {
		case s.Session.PTY != nil:
			opened["pty"] = s.Session.PTY.ID
		case s.Session.Exec != nil:
			opened["exec"] = s.Session.Exec.ID
		case s.Session.Lease != nil:
			opened["lease"] = s.Session.Lease.Resource
		}
	}
	// idle_s churn must not produce events; a state change must.
	f.host.mutate(func(h *fakeHost) { h.ptys[0].IdleS = 99; h.execs[0].State = "running" })
	var s v1.SessionEventData
	payload(t, events.next(t, ofType(v1.EventSession)), &s)
	if s.Change != "updated" || s.Session.Exec == nil || s.Session.Exec.State != "running" {
		t.Fatalf("%+v", s)
	}
	f.host.mutate(func(h *fakeHost) { h.ptys[0].State = "closed"; h.ptys[0].PID = 0 })
	payload(t, events.next(t, ofType(v1.EventSession)), &s)
	if s.Change != "closed" || s.Session.PTY == nil {
		t.Fatalf("%+v", s)
	}
	f.host.mutate(func(h *fakeHost) { h.ptys, h.execs, h.leases, h.warnings = nil, nil, nil, nil })
	// By now the poller is past its start-up, so the status change is announced, before the warning.
	var st v1.StatusResponse
	payload(t, events.next(t, func(e v1.Event) bool {
		if e.Type != v1.EventStatus {
			return false
		}
		payload(t, e, &st)
		return len(st.Warnings) == 0
	}), &st)
	payload(t, events.next(t, ofType(v1.EventWarning)), &w)
	if w.Change != "cleared" {
		t.Fatal(w)
	}
	released := map[string]string{}
	for len(released) < 3 {
		payload(t, events.next(t, ofType(v1.EventSession)), &s)
		switch {
		case s.Session.PTY != nil && s.Change == "released":
			released["pty"] = s.Change
		case s.Session.Exec != nil && s.Change == "closed":
			released["exec"] = s.Change
		case s.Session.Lease != nil && s.Change == "released":
			released["lease"] = s.Change
		}
	}
}

func Test_ControlPeerEvents(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Poll = 5 * time.Millisecond })
	for len(f.server.cachedPeers()) == 0 { // the first sample is the baseline
		time.Sleep(time.Millisecond)
	}
	events := f.stream("")
	next := func() v1.PeerEventData {
		var p v1.PeerEventData
		payload(t, events.next(t, ofType(v1.EventPeer)), &p)
		return p
	}
	f.host.mutate(func(h *fakeHost) { h.peers = []v1.Peer{peer("box", false)} })
	if p := next(); p.Change != "added" || p.Peer.Name != "box" || p.Peer.Self {
		t.Fatal(p)
	}
	f.host.mutate(func(h *fakeHost) { h.peers[0].Online = true })
	if p := next(); p.Change != "online" {
		t.Fatal(p)
	}
	f.host.mutate(func(h *fakeHost) { h.peers[0].Connection = "derp" })
	if p := next(); p.Change != "updated" || p.Peer.Connection != "derp" {
		t.Fatal(p)
	}
	f.host.mutate(func(h *fakeHost) { h.peers[0].Online = false })
	if p := next(); p.Change != "offline" {
		t.Fatal(p)
	}
	f.host.mutate(func(h *fakeHost) { h.peers = nil })
	if p := next(); p.Change != "removed" {
		t.Fatal(p)
	}
}

func Test_ControlLifecycleEvents(t *testing.T) {
	f := newFixture(t)
	first := f.stream(f.server.Bus().BootID() + ".0")
	e := first.next(t, ofType(v1.EventLifecycle))
	var l v1.LifecycleEventData
	payload(t, e, &l)
	if l.Change != "running" || e.Seq != 1 {
		t.Fatalf("%+v %+v", e, l)
	}
	f.server.Close()
	// closing flushes a stopping event before the stream ends
	var saw bool
	for e := range first.events {
		if e.Type == v1.EventLifecycle {
			var l v1.LifecycleEventData
			if json.Unmarshal(e.Data, &l) == nil && l.Change == "stopping" {
				saw = true
			}
		}
	}
	if !saw {
		t.Fatal("no stopping event")
	}
}
