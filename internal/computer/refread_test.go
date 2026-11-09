package computer

import (
	"context"
	"strings"
	"testing"
)

// readAdapter returns an adapter whose snapshot s9 holds a text field, a password field and a button.
func readAdapter(t *testing.T) (*AgentDesktop, *fakeRunner) {
	t.Helper()
	a, f := newADFake(t, 800, 600)
	reply := f.reply
	f.reply = func(args []string) string {
		switch args[0] {
		case "snapshot":
			return `{"ok":true,"data":{"snapshot_id":"s9","complete":true,"tree":{"role":"window","children":[{"role":"textfield","ref_id":"@s9:e1","value":"typed"},{"role":"AXSecureTextField","ref_id":"@s9:e2","value":"hunter2"},{"role":"button","ref_id":"@s9:e3","title":"Close"}]}}}`
		case "get":
			return `{"ok":true,"data":{"property":"` + args[3] + `","ref":"` + args[1] + `","value":"Close"}}`
		case "is":
			return `{"ok":true,"data":{"applicable":true,"property":"` + args[3] + `","ref":"` + args[1] + `","result":true}}`
		}
		return reply(args)
	}
	if _, err := a.Snapshot(context.Background(), SnapshotQuery{App: "Login"}); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func TestRefReadRunsGetAndIsWithTheProperty(t *testing.T) {
	a, f := readAdapter(t)
	ctx := context.Background()
	f.calls = nil
	res, err := a.RefRead(ctx, RefReadExecution{SnapshotID: "s9", Ref: "@s9:e3", Op: "get", Property: "title"})
	if err != nil || res.Ref != "@s9:e3" || res.Property != "title" || string(res.Value) != `"Close"` || res.Applicable != nil || res.Result != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if !hasCall(f.calls, []string{"get", "@s9:e3", "--property", "title"}) {
		t.Fatalf("get ran %v", f.calls)
	}
	f.calls = nil
	res, err = a.RefRead(ctx, RefReadExecution{SnapshotID: "s9", Ref: "@s9:e3", Op: "is", Property: "visible"})
	if err != nil || res.Applicable == nil || !*res.Applicable || res.Result == nil || !*res.Result || res.Value != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if !hasCall(f.calls, []string{"is", "@s9:e3", "--property", "visible"}) {
		t.Fatalf("is ran %v", f.calls)
	}
}

func TestRefReadKeepsAnInapplicableStateAsNotApplicable(t *testing.T) {
	a, f := readAdapter(t)
	f.reply = func(args []string) string {
		if args[0] == "is" {
			return `{"ok":true,"data":{"applicable":false,"property":"checked","ref":"@s9:e3","result":false}}`
		}
		return `{"ok":true,"data":{}}`
	}
	res, err := a.RefRead(context.Background(), RefReadExecution{SnapshotID: "s9", Ref: "@s9:e3", Op: "is", Property: "checked"})
	if err != nil || res.Applicable == nil || *res.Applicable || res.Result == nil || *res.Result {
		t.Fatalf("%+v %v", res, err)
	}
}

// Text and title of input and password elements are withheld before the CLI runs. Their states still read.
func TestRefReadWithholdsTextAndTitleOfInputAndPasswordElements(t *testing.T) {
	a, f := readAdapter(t)
	ctx := context.Background()
	for _, ref := range []string{"@s9:e1", "@s9:e2"} {
		for _, property := range []string{"text", "title"} {
			f.calls = nil
			if _, err := a.RefRead(ctx, RefReadExecution{SnapshotID: "s9", Ref: ref, Op: "get", Property: property}); errorCode(err) != "policy_denied" || len(f.calls) != 0 {
				t.Fatalf("%s %s must be withheld before the CLI runs: %v %v", ref, property, err, f.calls)
			}
		}
	}
	if _, err := a.RefRead(ctx, RefReadExecution{SnapshotID: "s9", Ref: "@s9:e3", Op: "get", Property: "text"}); err != nil {
		t.Fatalf("a button's text must read: %v", err)
	}
	if _, err := a.RefRead(ctx, RefReadExecution{SnapshotID: "s9", Ref: "@s9:e1", Op: "is", Property: "focused"}); err != nil {
		t.Fatalf("a state of a field must read: %v", err)
	}
}

// Refs the adapter has not read, values, and properties of the other op are refused before the CLI runs.
func TestRefReadRefusesUnknownRefsAndUnreadableProperties(t *testing.T) {
	a, f := readAdapter(t)
	ctx := context.Background()
	for name, r := range map[string]RefReadExecution{
		"ref never seen in a snapshot": {SnapshotID: "s9", Ref: "@s9:e7", Op: "get", Property: "role"},
		"snapshot never read":          {SnapshotID: "s8", Ref: "@s8:e1", Op: "get", Property: "role"},
		"value property":               {SnapshotID: "s9", Ref: "@s9:e3", Op: "get", Property: "value"},
		"is with text":                 {SnapshotID: "s9", Ref: "@s9:e3", Op: "is", Property: "text"},
		"get with a state flag":        {SnapshotID: "s9", Ref: "@s9:e3", Op: "get", Property: "visible"},
		"unknown op":                   {SnapshotID: "s9", Ref: "@s9:e3", Op: "click", Property: "text"},
		"ref of another snapshot":      {SnapshotID: "s9", Ref: "@s8:e3", Op: "get", Property: "role"},
	} {
		f.calls = nil
		if _, err := a.RefRead(ctx, r); err == nil || len(f.calls) != 0 {
			t.Fatalf("%s must be refused before the CLI runs: %v %v", name, err, f.calls)
		}
	}
}

func TestRefReadMapsEngineFailuresAndRejectsMalformedAnswers(t *testing.T) {
	a, f := readAdapter(t)
	ctx := context.Background()
	base := f.reply
	read := func(op, property string) error {
		_, err := a.RefRead(ctx, RefReadExecution{SnapshotID: "s9", Ref: "@s9:e3", Op: op, Property: property})
		return err
	}
	f.reply = func(args []string) string {
		if args[0] == "get" {
			return `{"ok":false,"error":{"code":"STALE_REF","message":"x","disposition":{"delivery":"not_delivered","retry":"safe"}}}`
		}
		return base(args)
	}
	if err := read("get", "title"); errorReason(err) != "stale_ref" {
		t.Fatalf("a stale ref must say so: %v", err)
	}
	f.reply = func(args []string) string {
		if args[0] == "is" {
			return `{"ok":false,"error":{"code":"AMBIGUOUS_TARGET","message":"x","disposition":{"delivery":"not_delivered","retry":"safe"}}}`
		}
		return base(args)
	}
	if err := read("is", "visible"); errorReason(err) != "ambiguous_target" {
		t.Fatalf("an ambiguous ref must say so: %v", err)
	}
	f.reply = func(args []string) string {
		if args[0] == "get" {
			return `{"ok":true,"data":{"property":"title","ref":"@s9:e3"}}`
		}
		return base(args)
	}
	if err := read("get", "title"); errorCode(err) != "desktop_unavailable" {
		t.Fatalf("a get without a value is an invalid answer: %v", err)
	}
	f.reply = func(args []string) string {
		if args[0] == "is" {
			return `{"ok":true,"data":{"applicable":true,"property":"visible","ref":"@s9:e3"}}`
		}
		return base(args)
	}
	if err := read("is", "visible"); errorCode(err) != "desktop_unavailable" {
		t.Fatalf("an is without a result is an invalid answer: %v", err)
	}
	f.reply = func(args []string) string {
		if args[0] == "get" {
			return `{"ok":true,"data":{"property":"text","ref":"@s9:e3","value":"` + strings.Repeat("x", maxReadBytes) + `"}}`
		}
		return base(args)
	}
	if err := read("get", "title"); errorCode(err) != "too_large" {
		t.Fatalf("an oversized answer must be refused, not cut: %v", errorCode(err))
	}
}
