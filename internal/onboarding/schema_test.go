package onboarding

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

func TestOnboardingSchemaMatchesPublicResults(t *testing.T) {
	raw, e := schemas.Files.ReadFile("onboarding.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc any
	if e = json.Unmarshal(raw, &doc); e != nil {
		t.Fatal(e)
	}
	c := jsonschema.NewCompiler()
	if e = c.AddResource("urn:xmesh:schema:onboarding:1", doc); e != nil {
		t.Fatal(e)
	}
	check := func(name string, value any) {
		t.Helper()
		schema, e := c.Compile("urn:xmesh:schema:onboarding:1#/$defs/" + name)
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		var normalized any
		_ = json.Unmarshal(b, &normalized)
		if e = schema.Validate(normalized); e != nil {
			t.Fatalf("%s mismatch: %v", name, e)
		}
	}
	s, in, h, _ := setupFixture(t)
	p, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	check("Input", p.Input)
	check("Plan", PublicPlan(p))
	j, e := s.CreateJob(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	check("Job", j)
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	j, e = s.LoadJob(j.ID)
	if e != nil {
		t.Fatal(e)
	}
	check("Job", j)
	check("Inspection", s.Inspect(context.Background()))
	check("Diagnostics", s.Doctor(context.Background()))
}
