package computer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// adReadmeCommands is the Commands table of the agent-desktop 1.0.2 README: 60 command names. The catalog
// must list each of them exactly once, so a command missing from the table cannot slip past review.
var adReadmeCommands = strings.Fields(`
	snapshot find get is screenshot list-surfaces
	click double-click triple-click right-click type set-value clear focus select toggle check uncheck expand collapse scroll scroll-to
	press hover drag mouse-move mouse-click mouse-wheel key-down key-up mouse-down mouse-up
	launch close-app list-apps list-windows list-displays focus-window resize-window move-window minimize maximize restore open-system-surface
	list-notifications dismiss-notification dismiss-all-notifications notification-action
	clipboard-get clipboard-set clipboard-clear
	wait
	session trace cursor-overlay
	status permissions version batch skills`)

// adAdapterMapping is what the adapter invokes today: upstream command to xmesh tool.
var adAdapterMapping = map[string]string{
	"permissions":   "computer.info",
	"list-displays": "computer.info",
	"screenshot":    "computer.observe",
	"snapshot":      "computer.snapshot",
	"mouse-move":    "computer.act",
	"mouse-click":   "computer.act",
	"mouse-wheel":   "computer.act",
	"drag":          "computer.act",
	"press":         "computer.act",
	"list-apps":     "computer.list_apps",
	"list-windows":  "computer.list_windows",
}

// adRefOps is the op enum of computer.ref_act and the upstream command each op runs.
var adRefOps = map[string]string{
	"click": "click", "right_click": "right-click", "focus": "focus", "set_value": "set-value", "type": "type",
	"clear": "clear", "select": "select", "toggle": "toggle", "check": "check", "uncheck": "uncheck",
	"expand": "expand", "collapse": "collapse", "scroll": "scroll", "scroll_to": "scroll-to",
}

func TestCatalogListsEveryCommandOfTheUpstreamTableOnce(t *testing.T) {
	if len(adReadmeCommands) != 60 {
		t.Fatalf("the reference table has %d names, want 60", len(adReadmeCommands))
	}
	names := make([]string, 0, len(adCatalogTable.Commands))
	for _, cmd := range adCatalogTable.Commands {
		names = append(names, cmd.Name)
	}
	want := slices.Clone(adReadmeCommands)
	slices.Sort(want)
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("catalog names differ from the upstream table\ncatalog: %v\ntable:   %v", names, want)
	}
}

func TestMappingsAreTheCallsTheAdapterMakes(t *testing.T) {
	for command, tool := range adAdapterMapping {
		cmd, ok := adCatalogTable.mapped([]string{command})
		if !ok || cmd.Tool != tool {
			t.Fatalf("%s maps to %q, want %q", command, cmd.Tool, tool)
		}
	}
	for op, command := range adRefOps {
		got, ok := adCatalogTable.commandForOp(op)
		if !ok || got != command {
			t.Fatalf("ref op %s runs %q, want %q", op, got, command)
		}
	}
	ops := MappedOps("computer.ref_act")
	if len(ops) != len(adRefOps) {
		t.Fatalf("computer.ref_act maps %d ops, want %d: %v", len(ops), len(adRefOps), ops)
	}
	for _, op := range ops {
		if _, ok := adRefOps[op]; !ok {
			t.Fatalf("unexpected mapped op %q", op)
		}
	}
	if want := len(adAdapterMapping) + len(adRefOps); len(adCatalogTable.Commands) != 60 || countMapped() != want {
		t.Fatalf("mapped commands = %d, want %d of 60", countMapped(), want)
	}
}

func countMapped() int {
	n := 0
	for _, cmd := range adCatalogTable.Commands {
		if cmd.Tool != "" {
			n++
		}
	}
	return n
}

func TestUnmappedCommandsAreExactlyTheRest(t *testing.T) {
	got := adCatalogTable.unmapped()
	if len(got) != 60-countMapped() {
		t.Fatalf("unmapped %d, want %d", len(got), 60-countMapped())
	}
	if !slices.IsSorted(got) {
		t.Fatalf("unmapped is not sorted: %v", got)
	}
	for _, name := range []string{"find", "batch", "clipboard-set", "launch", "wait"} {
		if !slices.Contains(got, name) {
			t.Fatalf("%s should be unmapped", name)
		}
	}
	for command := range adAdapterMapping {
		if slices.Contains(got, command) {
			t.Fatalf("%s is mapped but reported as unmapped", command)
		}
	}
}

func TestCatalogRejectsMalformedEntries(t *testing.T) {
	cases := map[string]string{
		"unknown key":             `{"schema":1,"table_version":"1.0.2","tols":{},"commands":[]}`,
		"unsupported schema":      `{"schema":2,"table_version":"1.0.2","commands":[]}`,
		"empty table version":     `{"schema":1,"table_version":"","commands":[]}`,
		"unknown risk":            `{"schema":1,"table_version":"1.0.2","commands":[{"name":"x","risk":"root"}]}`,
		"duplicate name":          `{"schema":1,"table_version":"1.0.2","commands":[{"name":"x","risk":"observe"},{"name":"x","risk":"observe"}]}`,
		"malformed name":          `{"schema":1,"table_version":"1.0.2","commands":[{"name":"Mouse Click","risk":"pointer"}]}`,
		"tool outside computer":   `{"schema":1,"table_version":"1.0.2","commands":[{"name":"x","risk":"observe","tool":"fs.read"}]}`,
		"op on a non-ref tool":    `{"schema":1,"table_version":"1.0.2","commands":[{"name":"x","risk":"observe","tool":"computer.observe","op":"click"}]}`,
		"ref tool without op":     `{"schema":1,"table_version":"1.0.2","commands":[{"name":"x","risk":"pointer","tool":"computer.ref_act"}]}`,
		"empty requirement":       `{"schema":1,"table_version":"1.0.2","tool_requires":{"computer.snapshot":[]},"commands":[]}`,
		"requirement on non-tool": `{"schema":1,"table_version":"1.0.2","tool_requires":{"snapshot":["refs"]},"commands":[]}`,
	}
	for name, raw := range cases {
		if _, err := parseADCatalog([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseADCatalog(agentDesktopCatalogJSON); err != nil {
		t.Fatalf("embedded catalog rejected: %v", err)
	}
}

func TestMappedRefusesUnlistedAndFlaggedInvocations(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
	}{
		{[]string{"permissions", "--request"}, true},
		{[]string{"--headed", "mouse-move", "--xy", "1,2"}, true},
		{[]string{"press", "a"}, true},
		{[]string{"batch", "{}"}, false},
		{[]string{"find", "x"}, false},
		{[]string{"--timeout", "5", "press", "a"}, false},
		{[]string{"--headed"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if _, ok := adCatalogTable.mapped(c.args); ok != c.ok {
			t.Errorf("mapped(%v) = %v, want %v", c.args, ok, c.ok)
		}
	}
}

func TestAdapterRefusesUnmappedCommandBeforeTheCLIRuns(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	_, aerr, err := a.call(context.Background(), "batch", "{}")
	if !errors.Is(err, errUnmappedCommand) || aerr != nil {
		t.Fatalf("err = %v, envelope error = %v", err, aerr)
	}
	if len(f.calls) != 0 {
		t.Fatalf("unmapped command reached the CLI: %v", f.calls)
	}
}

func TestInfoReportsTheUnmappedCommandsOfTheCatalog(t *testing.T) {
	a, _ := newADFake(t, 800, 600)
	info, err := a.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(info.UnmappedCommands, adCatalogTable.unmapped()) {
		t.Fatalf("unmapped_commands = %v", info.UnmappedCommands)
	}
	if len(info.UnmappedCommands) != 35 || !slices.Contains(info.UnmappedCommands, "find") || slices.Contains(info.UnmappedCommands, "mouse-click") {
		t.Fatalf("unmapped_commands = %v", info.UnmappedCommands)
	}
}

// Every command the adapter can issue, across all of its flows, passes the catalog gate. The gate refuses
// anything else before the fake sees it, so a flow that needed an unmapped command would fail here.
func TestEveryCommandTheAdapterIssuesIsMapped(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	ctx := context.Background()
	reply := f.reply
	f.reply = func(args []string) string {
		if args[0] == "snapshot" {
			return `{"ok":true,"data":{"snapshot_id":"s1","complete":true,"tree":{"role":"window","children":[{"role":"textfield","ref_id":"@s1:e1","value":"x"},{"role":"button","ref_id":"@s1:e2"}]}}}`
		}
		return reply(args)
	}
	info, err := a.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Capture(ctx, CaptureRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Snapshot(ctx, SnapshotQuery{App: "Notes", Skeleton: true}); err != nil {
		t.Fatal(err)
	}
	for _, op := range MappedOps("computer.ref_act") {
		r := RefExecution{Epoch: info.Epoch, ActionID: "a-" + op, SnapshotID: "s1", Ref: "@s1:e1", Op: op}
		switch op {
		case "set_value", "type", "select":
			r.Text = "hi"
		case "scroll":
			r.Direction, r.Amount = "down", 3
		}
		if res, err := a.RefAct(ctx, r); err != nil || res.State != "executed" {
			t.Fatalf("ref op %s: %+v %v", op, res, err)
		}
	}
	frame := Frame{Epoch: info.Epoch, DisplayID: "1", LayoutVersion: info.LayoutVersion, Width: 800, Height: 600, Region: Region{Width: 800, Height: 600}}
	x, y := adIntPtr(10), adIntPtr(20)
	res, err := a.Execute(ctx, Execution{Epoch: info.Epoch, Frame: &frame, Actions: []Action{
		{Type: "move", X: x, Y: y},
		{Type: "click", X: x, Y: y, Clicks: 2, Button: "right"},
		{Type: "scroll", X: x, Y: y, DeltaY: 2},
		{Type: "drag", X: x, Y: y, ToX: adIntPtr(30), ToY: adIntPtr(40), DurationMS: 5},
		{Type: "key", Keys: []string{"Control", "a"}},
		{Type: "text", Text: "hi"},
		{Type: "wait", DurationMS: 1},
	}})
	if err != nil || res.State != "executed" {
		t.Fatalf("foreground flow: %+v %v", res, err)
	}
	for _, call := range f.calls {
		if _, ok := adCatalogTable.mapped(call); !ok {
			t.Fatalf("adapter issued an unmapped command: %v", call)
		}
	}
	if len(f.calls) < 20 {
		t.Fatalf("the flows issued only %d commands", len(f.calls))
	}
}

func adIntPtr(v int) *int { return &v }
