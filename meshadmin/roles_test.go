package meshadmin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadNodeInfoPreservesSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xmesh.yaml")
	text := "schema_version: 2\nnode:\n  name: laptop\n  roles: [agent, gateway]\nagent:\n  authz:\n    tag:peer:\n      tools: [sys.*]\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	node, err := LoadNodeInfo(path)
	if err != nil || node.SchemaVersion != 2 || node.Name != "laptop" || !reflect.DeepEqual(node.Roles, []string{"agent", "gateway"}) || len(node.Authz[TagPeer].Tools) != 1 {
		t.Fatalf("node=%+v err=%v", node, err)
	}
}

func TestAnalyzeDeviceTrustRolesAndControlPlaneChecks(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	for _, tags := range [][]string{nil, {"tag:mobile"}, {TagDevice}} {
		nodes := []NodeInfo{{SchemaVersion: 2, Name: "laptop", Roles: []string{"gateway", "agent", "agent"}}}
		d := Device{ID: "1", Name: "laptop.ts.net", Authorized: true, Tags: tags}
		r := Analyze([]Device{d}, nodes, now)[0]
		if len(r.Issues) != 0 || !reflect.DeepEqual(r.Roles, []Role{RoleAgent, RoleGateway}) {
			t.Fatalf("tags=%v report=%+v", tags, r)
		}
		nodes[0].Roles = []string{"gateway"}
		if got := Analyze([]Device{d}, nodes, now)[0]; len(got.Issues) != 0 || !reflect.DeepEqual(got.Roles, []Role{RoleGateway}) {
			t.Fatalf("gateway report=%+v", got)
		}
		d.Authorized = false
		d.Expires = now.Add(-time.Hour)
		nodes[0].Roles = []string{"agent", "gateway", "ingress"}
		r = Analyze([]Device{d}, nodes, now)[0]
		codes := []string{}
		for _, issue := range r.Issues {
			codes = append(codes, issue.Code)
		}
		if !reflect.DeepEqual(codes, []string{"unauthorized", "key-expired"}) {
			t.Fatalf("control-plane checks=%+v", r.Issues)
		}
		d.Authorized = true
		d.Expires = time.Time{}
		d.Tags = append(d.Tags, TagIngress)
		if got := Analyze([]Device{d}, nodes, now)[0]; len(got.Issues) != 0 {
			t.Fatalf("ingress report=%+v", got)
		}
	}
}

func TestAnalyzeLegacyChecksRequireAnExplicitLegacyConfig(t *testing.T) {
	for _, version := range []int{0, 1} {
		nodes := []NodeInfo{{SchemaVersion: version, Name: "legacy", Roles: []string{"agent", "gateway"}}, {SchemaVersion: 2, Name: "other", Roles: []string{"agent"}}}
		devs := []Device{{Name: "legacy.ts.net", Authorized: true}, {Name: "unknown.ts.net", Authorized: true}}
		reports := Analyze(devs, nodes, time.Now())
		if len(reports[0].Issues) != 3 || reports[0].Issues[0].Code != "no-tags" || reports[0].Issues[1].Code != "missing-role-tag" || reports[0].Issues[2].Code != "missing-role-tag" || len(reports[1].Issues) != 0 {
			t.Fatalf("version=%d reports=%+v", version, reports)
		}
	}
}
