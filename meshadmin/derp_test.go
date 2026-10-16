package meshadmin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// conformance: CON-MESH-05 CTR-MESH-04
func TestAssignRegionIDs(t *testing.T) {
	out, err := AssignRegionIDs([]DERPRegionSpec{{Code: "a"}, {Code: "b", ID: 901}, {Code: "c"}}, map[int]bool{900: true})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ID != 902 || out[1].ID != 901 || out[2].ID != 903 {
		t.Fatalf("ids: %d %d %d", out[0].ID, out[1].ID, out[2].ID)
	}
	if _, err := AssignRegionIDs([]DERPRegionSpec{{Code: "a", ID: 901}, {Code: "b", ID: 901}}, nil); err == nil {
		t.Fatal("duplicate ids accepted")
	}
	full := map[int]bool{}
	for i := MinCustomRegionID; i <= MaxCustomRegionID; i++ {
		full[i] = true
	}
	if _, err := AssignRegionIDs([]DERPRegionSpec{{Code: "a"}}, full); err == nil {
		t.Fatal("allocation past 999")
	}
	ids, err := ExistingRegionIDs(`{"derpMap":{"regions":{"900":{},"905":null}}}`)
	if err != nil || !ids[900] || !ids[905] || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
}

// conformance: CON-MESH-05
func TestDERPSpecValidation(t *testing.T) {
	ok := DERPRegionSpec{Code: "lab", Name: "Lab", Hostname: "derp.example.com", IPv4: "198.51.100.1", IPv6: "2001:db8::1"}
	if w, err := ok.Validate(); err != nil || len(w) != 0 {
		t.Fatalf("%v %v", w, err)
	}
	bad := map[string]DERPRegionSpec{
		"scheme":   {Code: "a", Name: "A", Hostname: "https://derp.example.com"},
		"port":     {Code: "a", Name: "A", Hostname: "derp.example.com:443"},
		"id range": {Code: "a", Name: "A", Hostname: "derp.example.com", ID: 5},
		"v4":       {Code: "a", Name: "A", Hostname: "derp.example.com", IPv4: "2001:db8::1"},
		"v6":       {Code: "a", Name: "A", Hostname: "derp.example.com", IPv6: "198.51.100.1"},
		"code":     {Code: "My Region", Name: "A", Hostname: "derp.example.com"},
		"name":     {Code: "a", Hostname: "derp.example.com"},
	}
	for name, s := range bad {
		if _, err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	noIP := DERPRegionSpec{Code: "a", Name: "A", Hostname: "derp.example.com"}
	if w, _ := noIP.Validate(); len(w) != 1 {
		t.Fatalf("want a no-IP warning, got %v", w)
	}
}

// conformance: CON-MESH-05 CTR-MESH-04
func TestDERPFragmentWritesPolicy(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessWrite)
	ctx := context.Background()
	specs, _ := AssignRegionIDs([]DERPRegionSpec{{Code: "lab", Name: "Lab", Hostname: "derp.example.com", IPv4: "198.51.100.1"}}, nil)
	frag, warns, err := DERPFragment(specs, DERPOptions{OmitDefaultRegions: true})
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "single point of failure") {
		t.Fatalf("%v %v", warns, err)
	}
	plan, err := PlanPolicy(ctx, p, "derp", func(c string) (string, []string, error) { return ApplyFragment(c, frag, SectionDERP) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"omitDefaultRegions":true`, `"hostName":"derp.example.com"`, `"900"`, ManagedMarker + " derpMap"} {
		if !strings.Contains(strings.ReplaceAll(plan.After, ": ", ":"), want) {
			t.Errorf("policy lacks %s:\n%s", want, plan.After)
		}
	}
	// A failing probe restores the previous policy automatically.
	before, _ := p.GetPolicy(ctx)
	store := &MemStore{}
	failing := func(context.Context, DERPRegionSpec) ProbeResult {
		return ProbeResult{Host: "derp.example.com", OK: false}
	}
	res, err := ApplyDERP(ctx, p, store, plan, specs, failing)
	if !errors.Is(err, ErrProbeFailed) || !res.RolledBack {
		t.Fatalf("want rollback, got %v %+v", err, res)
	}
	after, _ := p.GetPolicy(ctx)
	if strings.TrimSpace(after.Text) != strings.TrimSpace(before.Text) {
		t.Fatalf("policy not restored:\n%s", after.Text)
	}
	// A passing probe keeps the write.
	plan2, _ := PlanPolicy(ctx, p, "derp", func(c string) (string, []string, error) { return ApplyFragment(c, frag, SectionDERP) })
	passing := func(context.Context, DERPRegionSpec) ProbeResult {
		return ProbeResult{Host: "derp.example.com", OK: true}
	}
	res, err = ApplyDERP(ctx, p, store, plan2, specs, passing)
	if err != nil || res.RolledBack || !res.Changed {
		t.Fatalf("%v %+v", err, res)
	}
	if got, _ := p.GetPolicy(ctx); !strings.Contains(got.Text, "derp.example.com") {
		t.Fatal("DERP write lost")
	}
}

func TestHeadscaleDERPConfigShape(t *testing.T) {
	specs, _ := AssignRegionIDs([]DERPRegionSpec{{Code: "lab", Name: "Lab", Hostname: "derp.example.com", IPv4: "198.51.100.1"}}, nil)
	out, warns, err := HeadscaleDERPConfig(specs, DERPOptions{}, "/etc/headscale/x.yaml")
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", warns, err)
	}
	if strings.Contains(out.Snippet, "urls: []") || !strings.Contains(out.Snippet, "- /etc/headscale/x.yaml") {
		t.Fatalf("snippet:\n%s", out.Snippet)
	}
	if !strings.Contains(string(out.File), "900:") || !strings.Contains(string(out.File), "hostname: derp.example.com") {
		t.Fatalf("file:\n%s", out.File)
	}
	if _, _, err := HeadscaleDERPConfig([]DERPRegionSpec{{Code: "x", Name: "X", Hostname: "d.example.com"}}, DERPOptions{}, "/x"); err == nil {
		t.Fatal("spec without id accepted")
	}
}
