package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/delegate"
)

// The refusals of the delegate tools carry the fields error.schema.json requires, so a caller can branch on them.
// conformance: CTR-ERR-01
func Test_CTRERR01_DelegateRefusalsMatchTheSchema(t *testing.T) {
	schema := errorSchema(t)
	m := delegate.New(nil)
	t.Cleanup(m.Close)
	off := false
	cases := []struct {
		name     string
		tool     string
		settings config.Delegate
		args     string
		code     string
	}{
		{"disabled", "delegate.list", config.Delegate{}, `{}`, "policy_denied"},
		{"write needs allow_write", "delegate.run", config.Delegate{Enabled: true}, `{"runner":"claude","prompt":"x","mode":"write"}`, "policy_denied"},
		{"sessions disabled", "delegate.send", config.Delegate{Enabled: true, AllowSessions: &off}, `{"session_id":"s","message":"m"}`, "policy_denied"},
		{"unknown task", "delegate.cancel", config.Delegate{Enabled: true}, `{"task_id":"t-missing"}`, "not_found"},
	}
	for _, c := range cases {
		_, toolErr := m.Tool(context.Background(), delegate.Request{Caller: "caller-a", Settings: c.settings}, c.tool, json.RawMessage(c.args))
		if toolErr == nil {
			t.Fatalf("%s: accepted", c.name)
		}
		body, err := json.Marshal(toolErr)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err = json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		if v["error"] != c.code {
			t.Errorf("%s: error = %v, want %s: %s", c.name, v["error"], c.code, body)
		}
		if err = schema.Validate(v); err != nil {
			t.Errorf("%s: violates error.schema.json: %v\n%s", c.name, err, body)
		}
	}
}
