package computer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type refDesktop struct {
	*fakeDesktop
	snaps []SnapshotQuery
	acts  []RefExecution
}

func (r *refDesktop) Snapshot(_ context.Context, q SnapshotQuery) (Snapshot, error) {
	r.snaps = append(r.snaps, q)
	return Snapshot{SnapshotID: "s1", Complete: true, Tree: json.RawMessage(`{"snapshot_id":"s1"}`)}, nil
}
func (r *refDesktop) RefAct(_ context.Context, e RefExecution) (ActionResult, error) {
	r.acts = append(r.acts, e)
	return ActionResult{State: "executed", Executed: 1}, nil
}

func refManager(t *testing.T, refs bool) (*Manager, *refDesktop) {
	t.Helper()
	d := &refDesktop{fakeDesktop: newFake()}
	if refs {
		d.info.Capabilities = append(d.info.Capabilities, "refs")
	}
	m := New(d, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	return m, d
}

func TestRefsUnsupportedWithoutCapability(t *testing.T) {
	m, _ := refManager(t, false)
	s := open(t, m, "caller", "observe")
	_, err := m.Snapshot(context.Background(), "caller", SnapshotRequest{SessionRequest: SessionRequest{SessionID: s.ID, Epoch: s.Epoch}})
	if errorReason(err) != "refs_unsupported" {
		t.Fatalf("%v", err)
	}
}

func errorReason(err error) string {
	if err == nil {
		return ""
	}
	if s := err.Error(); s == "" {
		return ""
	}
	type fielder interface{ Object() map[string]any }
	if f, ok := err.(fielder); ok {
		r, _ := f.Object()["reason"].(string)
		return r
	}
	return "?"
}

func TestSnapshotAndRefActLifecycle(t *testing.T) {
	m, d := refManager(t, true)
	s := open(t, m, "caller", "control")
	base := SessionRequest{SessionID: s.ID, Epoch: s.Epoch, Token: s.Token}
	ctx := context.Background()
	act := func(id, ref, op, text string) (ActionResult, error) {
		return m.RefAct(ctx, "caller", RefActRequest{SessionRequest: base, ActionID: id, SnapshotID: "s1", Ref: ref, Op: op, Text: text})
	}
	// A ref from a snapshot this session never took is stale.
	if _, err := act("a0", "@s1:e1", "click", ""); errorReason(err) != "stale_ref" {
		t.Fatalf("%v", err)
	}
	snap, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base, SnapshotQuery: SnapshotQuery{App: "Notes"}})
	if err != nil || snap.SnapshotID != "s1" || snap.Epoch != s.Epoch || !strings.Contains(snap.Notice, "untrusted") {
		t.Fatalf("%+v %v", snap, err)
	}
	r, err := act("a1", "@s1:e3", "set_value", "hi")
	if err != nil || r.State != "executed" || r.Executed != 1 || len(d.acts) != 1 || d.acts[0].Text != "hi" || d.acts[0].Token != s.Token {
		t.Fatalf("%+v %v %+v", r, err, d.acts)
	}
	// Same ID and request is idempotent; different request conflicts.
	if _, err = act("a1", "@s1:e3", "set_value", "hi"); err != nil || len(d.acts) != 1 {
		t.Fatalf("replay must not dispatch again: %v %d", err, len(d.acts))
	}
	if _, err = act("a1", "@s1:e4", "click", ""); errorReason(err) != "action_id_reused" {
		t.Fatalf("%v", err)
	}
	st, err := m.ActionStatus("caller", StatusRequest{SessionRequest: base, ActionID: "a1"})
	if err != nil || st.State != "executed" {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestRefActValidationAndAuthority(t *testing.T) {
	m, _ := refManager(t, true)
	obs := open(t, m, "caller", "observe")
	ctx := context.Background()
	if _, err := m.RefAct(ctx, "caller", RefActRequest{SessionRequest: SessionRequest{SessionID: obs.ID, Epoch: obs.Epoch}, ActionID: "a", SnapshotID: "s1", Ref: "@s1:e1", Op: "click"}); err == nil {
		t.Fatal("observe session must not act")
	}
	ctl := open(t, m, "other", "control")
	base := SessionRequest{SessionID: ctl.ID, Epoch: ctl.Epoch, Token: ctl.Token}
	for name, r := range map[string]RefActRequest{
		"ref of another snapshot": {ActionID: "a", SnapshotID: "s1", Ref: "@s2:e1", Op: "click"},
		"bare ref":                {ActionID: "a", SnapshotID: "s1", Ref: "@e1", Op: "click"},
		"unknown op":              {ActionID: "a", SnapshotID: "s1", Ref: "@s1:e1", Op: "drag"},
		"missing text":            {ActionID: "a", SnapshotID: "s1", Ref: "@s1:e1", Op: "set_value"},
		"stray text":              {ActionID: "a", SnapshotID: "s1", Ref: "@s1:e1", Op: "click", Text: "x"},
		"bad direction":           {ActionID: "a", SnapshotID: "s1", Ref: "@s1:e1", Op: "scroll", Direction: "sideways"},
	} {
		r.SessionRequest = base
		if _, err := m.RefAct(ctx, "other", r); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if _, err := m.Snapshot(ctx, "other", SnapshotRequest{SessionRequest: base, SnapshotQuery: SnapshotQuery{App: "-evil"}}); err == nil {
		t.Fatal("argument-like app must be rejected")
	}
	if _, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base}); err == nil {
		t.Fatal("another caller's session must be refused")
	}
}

func TestSnapshotScrubsSecretsAndGuardsRefs(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	f.reply = func(args []string) string {
		switch args[0] {
		case "permissions":
			return `{"ok":true,"data":{"accessibility":{"state":"granted"},"screen_recording":{"state":"granted"}}}`
		case "list-displays":
			return `{"ok":true,"data":[{"id":"1","bounds":{"x":0,"y":0,"width":800,"height":600},"scale":1}]}`
		case "snapshot":
			return `{"ok":true,"data":{"snapshot_id":"s9","complete":true,"tree":{"role":"window","children":[{"role":"textfield","ref_id":"@s9:e1","value":"visible"},{"role":"AXSecureTextField","ref_id":"@s9:e2","value":"hunter2"}]}}}`
		}
		return `{"ok":true,"data":{}}`
	}
	snap, err := a.Snapshot(context.Background(), SnapshotQuery{App: "Login"})
	if err != nil || snap.SnapshotID != "s9" || !snap.Complete {
		t.Fatalf("%+v %v", snap, err)
	}
	if strings.Contains(string(snap.Tree), "hunter2") || !strings.Contains(string(snap.Tree), "visible") {
		t.Fatalf("secret values must be withheld: %s", snap.Tree)
	}
	info, _ := a.Info(context.Background())
	if !contains(info.Capabilities, "refs") {
		t.Fatal("engine must advertise refs")
	}
	run := func(op, ref, text string) ActionResult {
		f.calls = nil
		r, err := a.RefAct(context.Background(), RefExecution{Epoch: info.Epoch, ActionID: "x", SnapshotID: "s9", Ref: ref, Op: op, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run("set_value", "@s9:e2", "pw"); r.State != "failed" || r.Error != "policy_denied" || hasCall(f.calls, []string{"set-value", "@s9:e2", "pw"}) {
		t.Fatalf("writing a secret field must be refused before dispatch: %+v %v", r, f.calls)
	}
	if r := run("click", "@s9:e7", ""); r.Error != "stale_ref" {
		t.Fatalf("unknown ref: %+v", r)
	}
	if r := run("set_value", "@s9:e1", "-x"); r.State != "executed" || !hasCall(f.calls, []string{"set-value", "@s9:e1", "--", "-x"}) {
		t.Fatalf("%+v %v", r, f.calls)
	}
	for _, c := range f.calls {
		for _, arg := range c {
			if arg == "--headed" {
				t.Fatalf("ref actions must stay headless: %v", c)
			}
		}
	}
	f.reply = func(args []string) string {
		if args[0] == "list-displays" {
			return `{"ok":true,"data":[{"id":"1","bounds":{"x":0,"y":0,"width":800,"height":600},"scale":1}]}`
		}
		if args[0] == "click" {
			return `{"ok":false,"error":{"code":"STALE_REF","message":"x","disposition":{"delivery":"not_delivered","retry":"safe"}}}`
		}
		return `{"ok":true,"data":{"accessibility":{"state":"granted"},"screen_recording":{"state":"granted"}}}`
	}
	if r := run("click", "@s9:e1", ""); r.State != "failed" || r.Error != "stale_ref" {
		t.Fatalf("%+v", r)
	}
}

func TestRefToolRevocationStopsQueuedActions(t *testing.T) {
	m, d := refManager(t, true)
	s := open(t, m, "caller", "control")
	base := SessionRequest{SessionID: s.ID, Epoch: s.Epoch, Token: s.Token}
	ctx := context.Background()
	if _, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base}); err != nil {
		t.Fatal(err)
	}
	// computer.act stays allowed; only computer.ref_act is revoked.
	m.SetAuthorization(func(_, tool string) bool { return tool != "computer.ref_act" })
	if _, err := m.RefAct(ctx, "caller", RefActRequest{SessionRequest: base, ActionID: "r1", SnapshotID: "s1", Ref: "@s1:e1", Op: "click"}); err == nil {
		t.Fatal("revoked ref_act must be refused")
	}
	if len(d.acts) != 0 {
		t.Fatalf("revoked tool must not dispatch: %v", d.acts)
	}
}

func TestSnapshotToolRevocation(t *testing.T) {
	m, _ := refManager(t, true)
	s := open(t, m, "caller", "observe")
	m.SetAuthorization(func(_, tool string) bool { return tool != "computer.snapshot" })
	if _, err := m.Snapshot(context.Background(), "caller", SnapshotRequest{SessionRequest: SessionRequest{SessionID: s.ID, Epoch: s.Epoch}}); err == nil {
		t.Fatal("revoked snapshot tool must be refused")
	}
}

func TestSnapshotRootMustBeOwnedAndRetentionIsRecencyBased(t *testing.T) {
	m, d := refManager(t, true)
	s := open(t, m, "caller", "observe")
	base := SessionRequest{SessionID: s.ID, Epoch: s.Epoch}
	ctx := context.Background()
	if _, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base, SnapshotQuery: SnapshotQuery{Root: "@other:e1"}}); errorReason(err) != "stale_ref" || len(d.snaps) != 0 {
		t.Fatalf("a root from another session must be refused before the engine runs: %v", err)
	}
	if _, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base, SnapshotQuery: SnapshotQuery{Root: "@s1:e1"}}); err != nil {
		t.Fatalf("own root must drill down: %v", err)
	}
	// Re-obtaining an ID moves it to the newest position.
	m.mu.Lock()
	m.sessions[s.ID].snapshots = []string{"s1", "a", "b"}
	m.mu.Unlock()
	if _, err := m.Snapshot(ctx, "caller", SnapshotRequest{SessionRequest: base, SnapshotQuery: SnapshotQuery{Root: "@s1:e1"}}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	got := m.sessions[s.ID].snapshots
	m.mu.Unlock()
	if len(got) != 3 || got[2] != "s1" {
		t.Fatalf("re-obtained snapshot must be most recent: %v", got)
	}
}

func TestRefActReportsUnsupportedEngineBeforeStaleRef(t *testing.T) {
	m, _ := refManager(t, false)
	s := open(t, m, "caller", "control")
	_, err := m.RefAct(context.Background(), "caller", RefActRequest{SessionRequest: SessionRequest{SessionID: s.ID, Epoch: s.Epoch, Token: s.Token}, ActionID: "a", SnapshotID: "s1", Ref: "@s1:e1", Op: "click"})
	if errorReason(err) != "refs_unsupported" {
		t.Fatalf("%v", err)
	}
}
