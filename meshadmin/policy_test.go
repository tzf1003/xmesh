package meshadmin

import (
	"encoding/json"
	"strings"
	"testing"
)

const userPolicy = `// Company policy. Do not edit without review.
{
	// who may own which tag
	"tagOwners": {
		"tag:db": ["group:ops"], // databases
	},

	"acls": [
		// ops can reach databases
		{"action": "accept", "src": ["group:ops"], "dst": ["tag:db:5432"]},
	],
	"groups": {"group:ops": ["alice@example.com"]},
}
`

// conformance: CON-MESH-04 CTR-MESH-02
func TestApplyFragmentPreservesUserContent(t *testing.T) {
	frag := Fragment{
		TagOwners: map[string][]string{"tag:device": {"autogroup:admin"}, "tag:ingress": {"autogroup:admin"}},
		ACLs:      []ACLRule{{Src: []string{"tag:ingress"}, Dst: []string{"tag:device:7433"}}},
	}
	out, warns, err := ApplyFragment(userPolicy, frag, SectionTagOwners, SectionACLs)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	for _, want := range []string{"// Company policy. Do not edit", "// who may own which tag", "// databases", "// ops can reach databases", `"group:ops": ["alice@example.com"]`, ManagedMarker} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost %q:\n%s", want, out)
		}
	}
	if _, err := StandardJSON(out); err != nil {
		t.Fatalf("not valid HuJSON: %v\n%s", err, out)
	}
	// Removing the managed fragment must give the original back byte for byte.
	back, _, err := ApplyFragment(out, Fragment{}, SectionTagOwners, SectionACLs)
	if err != nil {
		t.Fatal(err)
	}
	if back != userPolicy {
		t.Fatalf("round trip changed user text.\n--- want\n%s\n--- got\n%s", userPolicy, back)
	}
}

// conformance: CTR-MESH-02
func TestApplyFragmentIsIdempotentAndReplaces(t *testing.T) {
	a := Fragment{ACLs: []ACLRule{{Src: []string{"tag:peer"}, Dst: []string{"tag:device:7433"}}}}
	one, _, err := ApplyFragment(userPolicy, a, SectionACLs)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := ApplyFragment(one, a, SectionACLs)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("re-applying the same fragment changed the text:\n%s\n---\n%s", one, two)
	}
	b := Fragment{ACLs: []ACLRule{{Src: []string{"tag:admin"}, Dst: []string{"tag:device:7433"}}}}
	three, _, _ := ApplyFragment(two, b, SectionACLs)
	if strings.Contains(three, "tag:peer") || !strings.Contains(three, "tag:admin") {
		t.Fatalf("replace failed:\n%s", three)
	}
	if n := strings.Count(three, ManagedMarker); n != 1 {
		t.Fatalf("want 1 managed marker, got %d", n)
	}
}

func TestApplyFragmentEmptyAndMinimalPolicies(t *testing.T) {
	for name, text := range map[string]string{"empty": "", "braces": "{}", "no-trailing-comma": "{\n\t\"groups\": {}\n}\n"} {
		t.Run(name, func(t *testing.T) {
			out, _, err := ApplyFragment(text, Fragment{
				TagOwners: map[string][]string{"tag:device": {"alice@"}},
				ACLs:      []ACLRule{{Src: []string{"tag:device"}, Dst: []string{"tag:device:7433"}}},
			}, SectionTagOwners, SectionACLs)
			if err != nil {
				t.Fatal(err)
			}
			std, err := StandardJSON(out)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			var p struct {
				ACLs      []ACLRule           `json:"acls"`
				TagOwners map[string][]string `json:"tagOwners"`
			}
			if err := json.Unmarshal(std, &p); err != nil {
				t.Fatal(err)
			}
			if len(p.ACLs) != 1 || p.ACLs[0].Action != "accept" || len(p.TagOwners["tag:device"]) != 1 {
				t.Fatalf("unexpected result: %s", std)
			}
		})
	}
}

// conformance: CON-MESH-04
func TestApplyFragmentKeepsUserTagOwner(t *testing.T) {
	out, warns, err := ApplyFragment(userPolicy, Fragment{TagOwners: map[string][]string{"tag:db": {"autogroup:admin"}, "tag:device": {"autogroup:admin"}}}, SectionTagOwners)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "tag:db") {
		t.Fatalf("want a warning about tag:db, got %v", warns)
	}
	if !strings.Contains(out, `"tag:db": ["group:ops"]`) || strings.Contains(out, "autogroup:admin\"],\n\t\t// xmesh:managed tagOwners\n\t\t\"tag:db\"") {
		t.Fatalf("user tag owner was modified:\n%s", out)
	}
}

func TestRejectsNonObjectPolicy(t *testing.T) {
	if _, _, err := ApplyFragment("[1]", Fragment{}, SectionACLs); err == nil {
		t.Fatal("array root accepted")
	}
	if _, _, err := ApplyFragment("{ nope", Fragment{}, SectionACLs); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestDERPSectionRoundTrip(t *testing.T) {
	region := json.RawMessage(`{"regionID":900,"regionCode":"lab","regionName":"Lab","nodes":[{"name":"900a","regionID":900,"hostName":"derp.example.test","ipv4":"198.51.100.7"}]}`)
	omit := true
	out, _, err := ApplyFragment(userPolicy, Fragment{DERPRegions: map[int]json.RawMessage{900: region}, OmitDefaultRegions: &omit}, SectionDERP)
	if err != nil {
		t.Fatal(err)
	}
	std, err := StandardJSON(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var p struct {
		DERPMap struct {
			Omit    bool                       `json:"omitDefaultRegions"`
			Regions map[string]json.RawMessage `json:"regions"`
		} `json:"derpMap"`
	}
	_ = json.Unmarshal(std, &p)
	if !p.DERPMap.Omit || len(p.DERPMap.Regions) != 1 {
		t.Fatalf("derpMap not written: %s", std)
	}
	secs, _ := ManagedSections(out)
	if len(secs) != 1 || secs[0] != SectionDERP {
		t.Fatalf("sections = %v", secs)
	}
	// Removing regions leaves the (now empty) derpMap shell the first apply created.
	back, _, err := ApplyFragment(out, Fragment{}, SectionDERP)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(back, "900") {
		t.Fatalf("region not removed:\n%s", back)
	}
}

func TestTrailingCommentStaysWithItsElement(t *testing.T) {
	in := "{\n\t\"acls\": [\n\t\t{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]} // keep me\n\t]\n}\n"
	out, _, err := ApplyFragment(in, Fragment{ACLs: []ACLRule{{Src: []string{"tag:peer"}, Dst: []string{"tag:device:7433"}}}}, SectionACLs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"dst":["*:*"]}, // keep me`) {
		t.Fatalf("comment detached from its rule:\n%s", out)
	}
	back, _, _ := ApplyFragment(out, Fragment{}, SectionACLs)
	if StringsEqualIgnoringComma(back, in) == false {
		t.Fatalf("round trip lost content:\n%s", back)
	}
}

// StringsEqualIgnoringComma compares texts after dropping the optional comma
// that sits directly before a line comment.
func StringsEqualIgnoringComma(a, b string) bool {
	norm := func(s string) string { return strings.ReplaceAll(s, "}, //", "} //") }
	return norm(a) == norm(b)
}

func TestDERPOmitFlagFollowsLatestApply(t *testing.T) {
	region := json.RawMessage(`{"regionID":900,"regionCode":"lab","regionName":"Lab","nodes":[]}`)
	yes, no := true, false
	one, _, err := ApplyFragment(userPolicy, Fragment{DERPRegions: map[int]json.RawMessage{900: region}, OmitDefaultRegions: &yes}, SectionDERP)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := ApplyFragment(one, Fragment{DERPRegions: map[int]json.RawMessage{900: region}, OmitDefaultRegions: &no}, SectionDERP)
	if err != nil {
		t.Fatal(err)
	}
	std, _ := StandardJSON(two)
	var p struct {
		DERPMap struct {
			Omit bool `json:"omitDefaultRegions"`
		} `json:"derpMap"`
	}
	_ = json.Unmarshal(std, &p)
	if p.DERPMap.Omit {
		t.Fatalf("stale omitDefaultRegions:\n%s", two)
	}
	if strings.Count(two, "omitDefaultRegions") != 1 {
		t.Fatalf("omitDefaultRegions duplicated:\n%s", two)
	}
}
