package oauth

import (
	"encoding/json"
	"testing"
)

func TestComputerScopesAndMixedModeOpen(t *testing.T) {
	observe := []string{"computer:observe:mac"}
	control := []string{"computer:control:mac"}
	for _, s := range [][]string{observe, control, {"computer:clipboard-read:*"}, {"computer:clipboard-write:win"}} {
		if _, e := Expand(s); e != nil {
			t.Fatal(s, e)
		}
	}
	for _, s := range []string{"computer:*:*", "computer:admin:mac", "computer:control:", "computer:observe:bad/name"} {
		if _, e := Expand([]string{s}); e == nil {
			t.Fatal(s)
		}
	}
	if Allows(observe, "mac", "computer.act", "") || Allows(observe, "win", "computer.observe", "") {
		t.Fatal("scope escalation")
	}
	if !Allows(observe, "mac", "computer.open", "") || !Allows(control, "mac", "computer.observe", "") {
		t.Fatal("scope visibility")
	}
	for _, mode := range []string{"control", "observe"} {
		args := map[string]json.RawMessage{"mode": json.RawMessage(`"` + mode + `"`)}
		if AllowsArguments(observe, "mac", "computer.open", args) != (mode == "observe") {
			t.Fatal(mode)
		}
		if !AllowsArguments(control, "mac", "computer.open", args) {
			t.Fatal(mode)
		}
	}
	if Allows([]string{"computer:clipboard-read:mac"}, "mac", "computer.observe", "") {
		t.Fatal("clipboard scope exposed screen")
	}
}
