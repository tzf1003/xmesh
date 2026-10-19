//go:build policy_live

package meshadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This explicitly selected suite runs the shipped CLI against a real Headscale
// process and its throwaway SQLite database, never the user's control plane.
func TestPolicyDocumentLivePublish(t *testing.T) {
	if os.Getenv("XMESH_TEST_HEADSCALE_BIN") == "" || os.Getenv("XMESH_TEST_XMESH_BIN") == "" {
		t.Fatal("set XMESH_TEST_HEADSCALE_BIN and XMESH_TEST_XMESH_BIN for policy_live tests")
	}
	hs := startHeadscale(t, nil, "database")
	p, err := NewHeadscale(HeadscaleConfig{BaseURL: hs.url, APIKey: hs.apiKey})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := `{"acls":[{"action":"accept","src":["*"],"dst":["*:7433"]}]}`
	base, err := p.SetPolicy(ctx, seed, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cred := filepath.Join(dir, "credential.json")
	snapshots := filepath.Join(dir, "snapshots")
	if err := WriteCredentialFile(cred, Credential{Kind: KindHeadscale, Type: CredAPIKey, Secret: hs.apiKey, URL: hs.url, Access: AccessWrite}); err != nil {
		t.Fatal(err)
	}
	run := func(op string, input any, args ...string) ([]byte, error) {
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		argv := append([]string{"mesh", "policy", op, "--json", "--credential-file=" + cred, "--snapshot-dir=" + snapshots}, args...)
		cmd := exec.Command(os.Getenv("XMESH_TEST_XMESH_BIN"), argv...)
		cmd.Stdin = bytes.NewReader(body)
		return cmd.CombinedOutput()
	}
	next := strings.Replace(seed, "*:7433", "*:7434", 1)
	preview, err := run("publish", DocumentEdit{Text: next}, "--expect-version="+base.Version)
	if err != nil {
		t.Fatalf("preview: %v %s", err, preview)
	}
	var result struct {
		Plan    PolicyPlan
		Applied bool
		Result  ApplyResult
	}
	if err := json.Unmarshal(preview, &result); err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Plan.After != next || result.Plan.BaseVersion != base.Version {
		t.Fatal("preview changed document or base version")
	}
	current, err := p.GetPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != seed {
		t.Fatal("preview wrote policy")
	}
	applied, err := run("publish", DocumentEdit{Text: next}, "--expect-version="+base.Version, "--apply", "--yes")
	if err != nil {
		t.Fatalf("apply: %v %s", err, applied)
	}
	if err := json.Unmarshal(applied, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Result.SnapshotID == "" {
		t.Fatal("missing applied result or snapshot")
	}
	current, err = p.GetPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != next {
		t.Fatal("cloud readback differs from final draft")
	}
	store := DirStore{Dir: snapshots}
	saved, err := store.Load(result.Result.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Text != seed {
		t.Fatal("snapshot did not preserve original policy")
	}
	conflict, err := run("publish", DocumentEdit{Text: seed}, "--expect-version="+base.Version, "--apply", "--yes")
	if err == nil || !strings.Contains(string(conflict), ErrConflict.Error()) {
		t.Fatalf("stale write accepted: %v %s", err, conflict)
	}
	current, err = p.GetPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Text != next {
		t.Fatal("conflict overwrote policy")
	}
	invalid, err := run("publish", DocumentEdit{Text: `{"acls":`}, "--expect-version="+current.Version, "--apply", "--yes")
	if err == nil {
		t.Fatalf("invalid source published: %s", invalid)
	}
	if err := WriteCredentialFile(cred, Credential{Kind: KindHeadscale, Type: CredAPIKey, Secret: hs.apiKey, URL: hs.url, Access: AccessRead}); err != nil {
		t.Fatal(err)
	}
	denied, err := run("publish", DocumentEdit{Text: seed}, "--expect-version="+current.Version, "--apply", "--yes")
	if err == nil || !strings.Contains(string(denied), "insufficient_scope") {
		t.Fatalf("read-only credential wrote policy: %v %s", err, denied)
	}
}
