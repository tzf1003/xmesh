package gateway

import (
	"slices"
	"testing"
)

// conformance: CTR-DLG-06
func Test_CTRDLG06_DelegateToolsAreBuiltinAndNotListedByDefault(t *testing.T) {
	for _, name := range []string{"delegate.list", "delegate.run", "delegate.output", "delegate.send", "delegate.cancel"} {
		if !builtin(name) {
			t.Fatalf("%s is not served by the Agent itself", name)
		}
		if got := toolGroup(name); got != "delegate" {
			t.Fatalf("%s is in group %q, want delegate", name, got)
		}
	}
	if !slices.Contains(builtinGroups, "delegate") {
		t.Fatal("the delegate group is not a built-in group")
	}
	if slices.Contains(defaultListedGroups, "delegate") {
		t.Fatalf("the default listed groups %v include delegate", defaultListedGroups)
	}
}
