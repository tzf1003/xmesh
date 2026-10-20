package computer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

func TestDesktopErrorsConformToSharedEnvelope(t *testing.T) {
	b, _ := schemas.Files.ReadFile("error.schema.json")
	var doc any
	_ = json.Unmarshal(b, &doc)
	c := jsonschema.NewCompiler()
	if err := c.AddResource("urn:errors", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("urn:errors")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := manager(t)
	s := open(t, m, "a/1", "control")
	_, busy := m.Open(context.Background(), "b/2", OpenRequest{Mode: "control"})
	_, owned := m.Renew("b/2", RenewRequest{SessionRequest: request(s)})
	_, missing := m.Renew("a/1", RenewRequest{SessionRequest: SessionRequest{SessionID: "missing", Epoch: s.Epoch}})
	_, frame := m.Act(context.Background(), "a/1", ActRequest{SessionRequest: request(s), ActionID: "a", Actions: []Action{{Type: "click", X: point(1), Y: point(2)}}})
	_, notfound := m.ActionStatus("a/1", StatusRequest{SessionRequest: request(s), ActionID: "missing"})
	m.Configure(Settings{Enabled: true, MaxImageBytes: 1})
	_, large := m.Observe(context.Background(), "a/1", ObserveRequest{SessionRequest: request(s)})
	for _, err := range []error{busy, owned, missing, frame, notfound, large} {
		if err == nil {
			t.Fatal("expected error")
		}
		raw, _ := json.Marshal(Envelope(err).Object())
		var v any
		_ = json.Unmarshal(raw, &v)
		if e := schema.Validate(v); e != nil {
			t.Fatal(code(err), string(raw), e)
		}
	}
}
