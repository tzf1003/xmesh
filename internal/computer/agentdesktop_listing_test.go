package computer

import (
	"context"
	"slices"
	"testing"
)

// The two listings return the data the engine wrote, unchanged. The adapter runs them without a session.
func TestListingsRunTheirCommandAndKeepTheirData(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	reply := f.reply
	f.reply = func(args []string) string {
		if len(args) > 0 && (args[0] == "list-apps" || args[0] == "list-windows") {
			return `{"ok":true,"data":{"items":[{"name":"Notes"}]}}`
		}
		return reply(args)
	}
	for _, command := range []string{"list-apps", "list-windows"} {
		data, err := a.Lists(context.Background(), command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if string(data) != `{"items":[{"name":"Notes"}]}` {
			t.Fatalf("%s returned %s", command, data)
		}
		if got := f.calls[len(f.calls)-1]; !slices.Contains(got, command) {
			t.Fatalf("%s issued %v", command, got)
		}
	}
}

// Only the two listings can run through Lists. Any other command, mapped or not, is refused before the CLI runs.
func TestListsRefusesAnyOtherCommand(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	before := len(f.calls)
	for _, command := range []string{"list-displays", "click", "find", "get", "is", "snapshot", "list-apps --all"} {
		if _, err := a.Lists(context.Background(), command); err == nil {
			t.Fatalf("Lists(%q) ran", command)
		}
	}
	if len(f.calls) != before {
		t.Fatalf("refused listings still reached the CLI: %v", f.calls[before:])
	}
}

// Both tools appear only when the engine reports engine:agent-desktop.
func TestListingToolsRequireTheAgentDesktopEngine(t *testing.T) {
	for _, tool := range []string{"computer.list_apps", "computer.list_windows"} {
		if got := adCatalogTable.requires(tool); !slices.Equal(got, []string{"engine:agent-desktop"}) {
			t.Fatalf("%s requires %v", tool, got)
		}
	}
}
