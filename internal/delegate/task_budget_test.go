package delegate

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/config"
)

// A runner that prints empty events without end must not grow the log without bound: each event is charged for the
// record that holds it, not only for its text (CTR-DLG-03).
// conformance: CTR-DLG-03
func Test_CTRDLG03_EmptyEventsAreBounded(t *testing.T) {
	tk := &task{changed: make(chan struct{})}
	for i := 0; i < 2*maxEventBytes/eventOverhead; i++ {
		tk.appendLocked("assistant", "")
	}
	// The events that fit, then one system event that says the budget is spent.
	limit := maxEventBytes/eventOverhead + 1
	if n := len(tk.events); n > limit {
		t.Fatalf("kept %d events, want at most %d", n, limit)
	}
	last := tk.events[len(tk.events)-1]
	if last.typ != "system" || last.text != budgetNotice {
		t.Fatalf("the last event is %q %q, want the budget notice", last.typ, last.text)
	}
	if tk.appendLocked("assistant", "after the budget") {
		t.Fatal("an event after the budget notice was kept")
	}
}

// A final result over the budget is cut at a character boundary, keeps no more than the budget, and says so in the
// reply (CTR-DLG-05).
// conformance: CTR-DLG-05
func Test_CTRDLG05_FinalResultIsCappedAndFlagged(t *testing.T) {
	h := newHarness(t, config.Delegate{Enabled: true})
	tk := &task{kind: "claude", changed: make(chan struct{}), done: make(chan struct{}), cancelFn: func() {}}
	// Three-byte characters: the budget falls inside one of them, so the cut must step back to its start.
	line, err := json.Marshal(map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": false,
		"result":   strings.Repeat("好", maxEventBytes/3+10),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	h.m.running++
	h.m.mu.Unlock()
	h.m.consume(tk, line)
	h.m.finish(tk, 0, false, nil)

	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	out := tk.runViewLocked()
	result, _ := out["result"].(string)
	if len(result) > maxEventBytes || !utf8.ValidString(result) {
		t.Fatalf("result is %d bytes, valid UTF-8 %v; want at most %d bytes of valid UTF-8", len(result), utf8.ValidString(result), maxEventBytes)
	}
	if out["result_truncated"] != true {
		t.Fatalf("the reply does not say that the result was truncated: %v", out)
	}
}
