package meshadmin

import (
	"strings"
	"testing"
	"time"
)

func testDevices() []Device {
	return []Device{
		{ID: "1", Name: "gw.tail.ts.net", Hostname: "gw", Tags: []string{TagPeer}, Addresses: []string{"100.64.0.1"}, Authorized: true},
		{ID: "2", Name: "box.tail.ts.net", Hostname: "box", Tags: []string{TagDevice}, Addresses: []string{"100.64.0.2"}, Authorized: true},
		{ID: "3", Name: "edge.tail.ts.net", Hostname: "edge", Tags: []string{TagIngress}, Addresses: []string{"100.64.0.3"}, Authorized: true},
		{ID: "4", Name: "pc.tail.ts.net", Hostname: "pc", User: "alice@example.com", Addresses: []string{"100.64.0.4"}, Authorized: true},
	}
}

func cell(t *testing.T, m Matrix, src, dst string) Cell {
	t.Helper()
	for _, c := range m.Cells {
		if c.Src == src && c.Dst == dst {
			return c
		}
	}
	t.Fatalf("no cell %s -> %s", src, dst)
	return Cell{}
}

// conformance: CTR-MESH-03
func TestMatrixMergesBothLayers(t *testing.T) {
	policy := `{
		"groups": {"group:staff": ["alice@example.com"]},
		"acls": [
			{"action":"accept","src":["tag:peer"],"dst":["tag:device:7433"]},
			{"action":"accept","src":["tag:ingress"],"dst":["tag:device:8000-9000"]},
			{"action":"accept","src":["group:staff"],"dst":["tag:device:*"]},
		],
	}`
	nodes := []NodeInfo{{Name: "box", Roles: []string{"agent"}, Authz: map[string]Grant{
		TagPeer:    {Backends: []string{"*"}, Tools: []string{"*"}},
		TagIngress: {Backends: []string{"ida"}, Tools: []string{"fs.*", "sys.*"}},
		TagAdmin:   {Tools: []string{"sys.*"}},
	}}}
	m, err := BuildMatrix(policy, testDevices(), nodes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Cells) != 3 { // only "box" is an agent
		t.Fatalf("cells: %+v", m.Cells)
	}
	c := cell(t, m, "gw", "box")
	if c.Network != Allow || c.Tools != Allow || c.Mismatch != "" || c.AllowedTools[0] != "*" {
		t.Fatalf("gw->box: %+v", c)
	}
	// ingress: authz allows tools but the rule only opens ports 8000-9000, not 7433.
	c = cell(t, m, "edge", "box")
	if c.Network != Deny || c.Tools != Allow || !strings.Contains(c.Mismatch, "network policy blocks") {
		t.Fatalf("edge->box: %+v", c)
	}
	// a user-owned node reaches the agent through the group but has no authz entry.
	c = cell(t, m, "pc", "box")
	if c.Network != Allow || c.Tools != Deny || !strings.Contains(c.Mismatch, "agent.authz has no usable entry") {
		t.Fatalf("pc->box: %+v", c)
	}
}

func TestMatrixUnknownAliasAndMissingAuthz(t *testing.T) {
	policy := `{"acls":[{"action":"accept","src":["autogroup:admin"],"dst":["tag:device:7433"]}]}`
	m, err := BuildMatrix(policy, testDevices(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := cell(t, m, "gw", "box")
	if c.Network != Unknown || c.Tools != Unknown || c.Mismatch != "" {
		t.Fatalf("unresolvable alias must stay unknown, not guessed: %+v", c)
	}
	if m, _ = BuildMatrix("", testDevices(), nil, 0); cell(t, m, "gw", "box").Network != Unknown {
		t.Fatal("empty policy must be unknown")
	}
}

func TestMatrixGrantsHostsAndCIDR(t *testing.T) {
	policy := `{
		"hosts": {"home": "100.64.0.4/32"},
		"grants": [{"src":["home"],"dst":["tag:device"],"ip":["tcp:7433"]},
		           {"src":["100.64.0.3"],"dst":["tag:device"],"ip":["udp:7433"]}],
	}`
	m, err := BuildMatrix(policy, testDevices(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c := cell(t, m, "pc", "box"); c.Network != Allow || !strings.HasPrefix(c.NetworkNote, "grants[0]") {
		t.Fatalf("host alias grant: %+v", c)
	}
	if c := cell(t, m, "edge", "box"); c.Network != Deny {
		t.Fatalf("udp-only grant must not admit tcp: %+v", c)
	}
}

func TestSplitDstAndPorts(t *testing.T) {
	cases := []struct{ in, alias, ports string }{
		{"tag:device:7433", "tag:device", "7433"},
		{"*:*", "*", "*"},
		{"tag:device", "tag:device", "*"},
		{"100.64.0.0/10:80,443", "100.64.0.0/10", "80,443"},
		{"fd7a::1:7433", "fd7a::1", "7433"},
	}
	for _, c := range cases {
		a, p := splitDst(c.in)
		if a != c.alias || p != c.ports {
			t.Errorf("splitDst(%q) = %q,%q want %q,%q", c.in, a, p, c.alias, c.ports)
		}
	}
	if !portsCover("1000-2000,7433", 7433) || portsCover("80,443", 7433) || !portsCover("*", 1) {
		t.Fatal("portsCover")
	}
}

// conformance: CTR-MESH-03
func TestAnalyzeFlagsTagAndKeyProblems(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	devs := []Device{
		{ID: "1", Name: "a.ts.net", Hostname: "a", Authorized: true},                                                              // untagged
		{ID: "2", Name: "b.ts.net", Hostname: "b", Tags: []string{TagPeer}, Authorized: true},                                     // config says agent
		{ID: "3", Name: "c.ts.net", Hostname: "c", Tags: []string{TagDevice}, Authorized: true, Expires: now.Add(-time.Hour)},     // expired
		{ID: "4", Name: "d.ts.net", Hostname: "d", Tags: []string{TagDevice}, Authorized: true, Expires: now.Add(48 * time.Hour)}, // expiring
		{ID: "5", Name: "e.ts.net", Hostname: "e", Tags: []string{TagDevice, TagIngress}, Authorized: false},                      // unauthorised
	}
	nodes := []NodeInfo{{Name: "b", Roles: []string{"agent", "gateway"}}, {Name: "e", Roles: []string{"gateway"}}}
	got := map[string][]string{}
	for _, r := range Analyze(devs, nodes, now) {
		for _, i := range r.Issues {
			got[r.Device.Hostname] = append(got[r.Device.Hostname], i.Code)
		}
	}
	want := map[string]string{"a": "no-tags", "b": "missing-role-tag", "c": "key-expired", "d": "key-expiring", "e": "unauthorized"}
	for h, code := range want {
		found := false
		for _, c := range got[h] {
			found = found || c == code
		}
		if !found {
			t.Errorf("%s: want issue %s, got %v", h, code, got[h])
		}
	}
	if !contains(got["e"], "missing-role-tag") || !contains(got["e"], "unexpected-device-tag") {
		t.Errorf("e: %v", got["e"])
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// conformance: CTR-MESH-02
func TestTemplates(t *testing.T) {
	for _, name := range TemplateNames() {
		f, err := BuildTemplate(name, TemplateOptions{Owner: "autogroup:admin"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.TagOwners) < 4 || len(f.ACLs) == 0 {
			t.Fatalf("%s: %+v", name, f)
		}
		// evaluate the template with the matrix evaluator against a canonical fleet
		text, _, err := ApplyFragment("", f, SectionTagOwners, SectionACLs)
		if err != nil {
			t.Fatal(err)
		}
		devs := []Device{
			{ID: "d", Hostname: "dev", Tags: []string{TagDevice}, Addresses: []string{"100.64.0.2"}},
			{ID: "p", Hostname: "peer", Tags: []string{TagPeer}, Addresses: []string{"100.64.0.1"}},
			{ID: "i", Hostname: "ing", Tags: []string{TagIngress}, Addresses: []string{"100.64.0.3"}},
			{ID: "t", Hostname: "target", Tags: []string{TagDevice, TagIngressTarget}, Addresses: []string{"100.64.0.5"}},
		}
		m, err := BuildMatrix(text, devs, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		net := func(src, dst string) Decision { return cell(t, m, src, dst).Network }
		if net("peer", "dev") != Allow || net("ing", "dev") == Unknown {
			t.Fatalf("%s: peer->dev %v", name, net("peer", "dev"))
		}
		switch name {
		case TemplatePersonal:
			if net("target", "dev") != Allow || net("ing", "dev") != Allow {
				t.Fatalf("personal: devices and ingress must reach agents")
			}
		case TemplateTeam:
			if net("target", "dev") != Deny || net("ing", "dev") != Allow {
				t.Fatalf("team: plain devices must not originate; ingress must reach agents")
			}
		case TemplateRestricted:
			if net("ing", "dev") != Deny || net("ing", "target") != Allow {
				t.Fatalf("restricted: ingress must reach only ingress-target (dev=%v target=%v)", net("ing", "dev"), net("ing", "target"))
			}
		}
	}
	if _, err := BuildTemplate("nope", TemplateOptions{Owner: "alice@"}); err == nil {
		t.Fatal("unknown template accepted")
	}
	if _, err := BuildTemplate(TemplateTeam, TemplateOptions{Owner: "rm -rf /"}); err == nil {
		t.Fatal("bad owner accepted")
	}
}

func TestCanonicalTags(t *testing.T) {
	got, err := CanonicalTags([]string{"tag:b", "tag:a", "tag:b"})
	if err != nil || strings.Join(got, ",") != "tag:a,tag:b" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"device", "tag:", "tag:a b", "tag:-x", "TAG:x"} {
		if _, err := CanonicalTags([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
