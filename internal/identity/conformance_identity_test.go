package identity

import (
	"strings"
	"testing"
)

// caller_id is node_id "/" mcp_session_id; each part matches
// [A-Za-z0-9._:-]{1,128}. Comparison is byte equality (case matters) and the
// concatenation is never truncated.
// conformance: CTR-ID-01
func Test_CTRID01_CallerIDFormAndCharset(t *testing.T) {
	long := strings.Repeat("n", 128)
	valid := []string{
		"node/session",
		"nABC123.local:9/Mcp-Session_1",
		"a/b",
		"local/init",
		"stable-self/0f3c9a8e-1d2b-4c5d-8e7f-aabbccddeeff",
		long + "/" + long, // the longest permitted form, 257 bytes
		"0/0",
	}
	for _, id := range valid {
		if !Valid(id) {
			t.Errorf("%q must be valid", id)
		}
		if err := NewRegistry().Add(Caller{ID: id}); err != nil {
			t.Errorf("%q was refused by the registry: %v", id, err)
		}
	}
	invalid := []string{
		"",
		"/",
		"node",
		"node/",
		"/session",
		"node/session/extra", // a second separator is not part of either charset
		"node//session",
		strings.Repeat("n", 129) + "/s",
		"n/" + strings.Repeat("s", 129),
		"no de/session",
		"node/ses\nsion",
		"node/ses\x00sion",
		"node/séssion",
		"node\\x/session",
		"node/session ",
		" node/session",
		"node/se@ssion",
	}
	for _, id := range invalid {
		if Valid(id) {
			t.Errorf("%q must be invalid", id)
		}
		if err := NewRegistry().Add(Caller{ID: id}); err == nil {
			t.Errorf("%q was accepted by the registry", id)
		}
	}

	// No truncation: the registry returns the identity unchanged.
	reg := NewRegistry()
	full := long + "/" + long
	if err := reg.Add(Caller{ID: full}); err != nil {
		t.Fatal(err)
	}
	got, ok := reg.Get(full)
	if !ok || got.ID != full || Node(got.ID) != long {
		t.Fatalf("the identity changed in the registry: %q", got.ID)
	}

	// Equality is byte equality; no case folding.
	if err := reg.Add(Caller{ID: "Node/Session"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Caller{ID: "node/session"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("NODE/SESSION"); ok {
		t.Error("lookup folded case")
	}
	upper, _ := reg.Get("Node/Session")
	lower, _ := reg.Get("node/session")
	if upper.ID == lower.ID || Budget(upper) == Budget(lower) {
		t.Errorf("callers differing only in case were merged: %q %q", Budget(upper), Budget(lower))
	}
}

// The node part of a peer identity comes from the transport and must itself fit
// the charset; an unusable node ID gives no principal at all.
// conformance: CTR-ID-01
func Test_CTRID01_PeerNodeIDMustFitTheCharset(t *testing.T) {
	for _, node := range []string{"", "a/b", "no de", strings.Repeat("n", 129), "n\x00"} {
		if _, err := FromPeer(node, nil, ""); err == nil {
			t.Errorf("node %q was accepted", node)
		}
	}
	p, err := FromPeer("nNodeID:1.x", []string{"tag:peer"}, "")
	if err != nil || p.Node != "nNodeID:1.x" || p.OAuthClient != "" {
		t.Fatalf("%v %+v", err, p)
	}
	// An ingress peer must come with its OAuth client from the request header.
	if _, err := FromPeer("ingress-node", []string{"tag:ingress"}, ""); err == nil {
		t.Error("an ingress session without an OAuth client was accepted")
	}
	p, err = FromPeer("ingress-node", []string{"tag:ingress"}, "client-7")
	if err != nil || p.OAuthClient != "client-7" {
		t.Fatalf("%v %+v", err, p)
	}
	// The header is only believed from ingress peers.
	p, err = FromPeer("plain-node", []string{"tag:peer"}, "client-7")
	if err != nil || p.OAuthClient != "" {
		t.Errorf("an OAuth client claimed by a non-ingress peer was recorded: %+v %v", p, err)
	}
}
