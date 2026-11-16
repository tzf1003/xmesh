package delegate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// A call that read the switch while it was on, and whose switch went off before the call registered its task, starts
// nothing. A task registered after the switch went off would have no cancel, since the switch refuses the cancel too.
// conformance: CTR-DLG-06
func Test_CTRDLG06_CallThatRacedTheSwitchStartsNothing(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"sleepy": fakeRunner(t, "sleep")}))
	req := h.request(callerA)
	h.m.CancelAll()
	raw, err := json.Marshal(map[string]any{"runner": "sleepy", "prompt": "x", "wait_ms": 0})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.m.Tool(context.Background(), req, "delegate.run", raw)
	h.wantCode(err, "policy_denied")
	h.m.mu.Lock()
	n := len(h.m.tasks) + h.m.running
	h.m.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d tasks registered after the switch went off", n)
	}
}
