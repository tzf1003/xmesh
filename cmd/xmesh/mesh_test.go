package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/tzf1003/xmesh/meshadmin"
)

const cliSecret = "tskey-api-CLI-SECRET-VALUE"

// cliTailscale is a compact Tailscale API fake for the CLI tests.
type cliTailscale struct {
	mu      sync.Mutex
	policy  string
	devices []map[string]any
	writeOK bool // false makes every mutation answer 403 like a read-only credential
}

func newCLITailscale(t *testing.T) (*cliTailscale, string) {
	f := &cliTailscale{policy: "// base\n{\n\t\"groups\": {},\n}\n", writeOK: true, devices: []map[string]any{
		{"id": "7", "name": "box.t.ts.net", "hostname": "box", "addresses": []string{"100.64.0.7"}, "tags": []string{"tag:device"}, "authorized": true, "connectedToControl": true, "nodeKey": "nodekey:" + strings.Repeat("ab", 32), "keyExpiryDisabled": true},
		{"id": "8", "name": "gw.t.ts.net", "hostname": "gw", "addresses": []string{"100.64.0.8"}, "tags": []string{}, "authorized": true, "nodeKey": "nodekey:" + strings.Repeat("cd", 32), "keyExpiryDisabled": true},
	}}
	etag := func() string { s := sha256.Sum256([]byte(f.policy)); return `"` + hex.EncodeToString(s[:6]) + `"` }
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+cliSecret {
			http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/api/v2/")
		if r.Method != http.MethodGet && !strings.HasSuffix(p, "/validate") && !f.writeOK {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case p == "tailnet/-/devices":
			_ = json.NewEncoder(w).Encode(map[string]any{"devices": f.devices})
		case strings.HasSuffix(p, "/tags"):
			var b struct{ Tags []string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			for _, d := range f.devices {
				if "device/"+d["id"].(string)+"/tags" == p {
					d["tags"] = b.Tags
				}
			}
			_, _ = io.WriteString(w, "{}")
		case strings.HasPrefix(p, "device/"):
			for _, d := range f.devices {
				if "device/"+d["id"].(string) == p {
					_ = json.NewEncoder(w).Encode(d)
				}
			}
		case p == "tailnet/-/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "k9", "key": "tskey-auth-FRESH-AUTH-KEY", "expires": "2026-12-01T00:00:00Z"})
		case p == "tailnet/-/acl" && r.Method == http.MethodGet:
			w.Header().Set("ETag", etag())
			_, _ = io.WriteString(w, f.policy)
		case p == "tailnet/-/acl":
			b, _ := io.ReadAll(r.Body)
			if m := r.Header.Get("If-Match"); m != "" && m != etag() {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			f.policy = string(b)
			w.Header().Set("ETag", etag())
			_, _ = io.WriteString(w, "{}")
		case p == "tailnet/-/acl/validate":
			_, _ = io.WriteString(w, "{}")
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func setupMesh(t *testing.T) (*cliTailscale, string) {
	t.Helper()
	f, url := newCLITailscale(t)
	store := &meshadmin.MemoryStore{}
	cred := meshadmin.Credential{Kind: meshadmin.KindTailscale, Type: meshadmin.CredToken, URL: url, Secret: cliSecret}
	_ = store.Put("default", meshadmin.AccessRead, cred)
	_ = store.Put("default", meshadmin.AccessWrite, cred)
	old, oldHTTP := meshCredentials, meshHTTP
	meshCredentials = store
	t.Cleanup(func() { meshCredentials, meshHTTP = old, oldHTTP })
	return f, t.TempDir()
}

var outputs []string // every byte the CLI printed in this test binary, scanned for secrets at the end

func runMesh(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := meshRun(args, &out, strings.NewReader(stdin))
	outputs = append(outputs, out.String())
	if err != nil {
		outputs = append(outputs, err.Error())
	}
	return out.String(), err
}

func TestMain(m *testing.M) {
	code := m.Run()
	for _, o := range outputs {
		if strings.Contains(o, cliSecret) || strings.Contains(o, "FRESH-AUTH-KEY") {
			println("FAIL: a CLI output contained a credential or an auth key")
			code = 1
		}
	}
	os.Exit(code)
}

func Test_MeshStatusJSON(t *testing.T) {
	setupMesh(t)
	out, err := runMesh(t, "", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Provider string `json:"provider"`
		Devices  []struct {
			Device struct{ Name string }
			Issues []struct{ Code string }
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.Provider != "tailscale" || len(doc.Devices) != 2 {
		t.Fatalf("%+v", doc)
	}
	var untagged bool
	for _, d := range doc.Devices {
		for _, i := range d.Issues {
			untagged = untagged || (d.Device.Name == "gw.t.ts.net" && i.Code == "no-tags")
		}
	}
	if !untagged {
		t.Fatalf("gw has no tags and should be flagged:\n%s", out)
	}
	if human, err := runMesh(t, "", "status"); err != nil || !strings.Contains(human, "box.t.ts.net") {
		t.Fatalf("human output: %v\n%s", err, human)
	}
}

// conformance: CON-MESH-03
func Test_MeshPolicyTemplatePreviewThenApply(t *testing.T) {
	f, dir := setupMesh(t)
	snap := filepath.Join(dir, "snaps")
	prev, err := runMesh(t, "", "policy", "--template", "team", "--owner", "autogroup:admin", "--snapshot-dir", snap, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var pv struct {
		Plan    meshadmin.PolicyPlan `json:"plan"`
		Applied bool                 `json:"applied"`
	}
	_ = json.Unmarshal([]byte(prev), &pv)
	if pv.Applied || !pv.Plan.Changed || !strings.Contains(f.policy, "// base") || strings.Contains(f.policy, meshadmin.ManagedMarker) {
		t.Fatalf("preview must not write: %+v", pv)
	}
	// A stale version is refused, the matching one applies.
	if _, err := runMesh(t, "", "policy", "--template", "team", "--owner", "autogroup:admin", "--snapshot-dir", snap, "--apply", "--expect-version", `"stale"`); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("stale --expect-version: %v", err)
	}
	if _, err := runMesh(t, "", "policy", "--template", "team", "--owner", "autogroup:admin", "--snapshot-dir", snap, "--apply", "--expect-version", pv.Plan.BaseVersion, "--json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.policy, meshadmin.ManagedMarker) || !strings.Contains(f.policy, "// base") {
		t.Fatalf("policy not updated or user comment lost:\n%s", f.policy)
	}
	list, _ := runMesh(t, "", "policy", "--snapshots", "--snapshot-dir", snap, "--json")
	var sl struct{ Snapshots []meshadmin.Snapshot }
	_ = json.Unmarshal([]byte(list), &sl)
	if len(sl.Snapshots) != 1 {
		t.Fatalf("want one snapshot: %s", list)
	}
	if _, err := runMesh(t, "", "policy", "--rollback", sl.Snapshots[0].ID, "--snapshot-dir", snap, "--apply"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.policy, meshadmin.ManagedMarker) {
		t.Fatalf("rollback did not restore:\n%s", f.policy)
	}
}

// conformance: RUL-22
func Test_MeshReadOnlyCredentialIsRefused(t *testing.T) {
	f, dir := setupMesh(t)
	f.writeOK = false
	_, err := runMesh(t, "", "policy", "--template", "personal", "--owner", "autogroup:admin", "--snapshot-dir", dir, "--apply")
	if err == nil || !strings.Contains(err.Error(), "credential rejected") {
		t.Fatalf("want a rejected-credential error, got %v", err)
	}
}

func Test_MeshTagsSetNeedsTagOwners(t *testing.T) {
	f, _ := setupMesh(t)
	if _, err := runMesh(t, "", "tags", "--device", "gw", "--set", "tag:peer", "--apply"); err == nil || !strings.Contains(err.Error(), "tagOwners") {
		t.Fatalf("tag without owner must be refused: %v", err)
	}
	f.policy = "{\n\t\"tagOwners\": {\"tag:peer\": [\"autogroup:admin\"]},\n}\n"
	if out, err := runMesh(t, "", "tags", "--device", "gw", "--set", "tag:peer"); err != nil || !strings.Contains(out, "仅预览") {
		t.Fatalf("preview: %v %s", err, out)
	}
	if f.devices[1]["tags"].([]string) != nil && len(f.devices[1]["tags"].([]string)) != 0 {
		t.Fatal("preview wrote tags")
	}
	if _, err := runMesh(t, "", "tags", "--device", "gw", "--set", "tag:peer", "--apply"); err != nil {
		t.Fatal(err)
	}
	if got := f.devices[1]["tags"].([]string); len(got) != 1 || got[0] != "tag:peer" {
		t.Fatalf("tags: %v", got)
	}
	if _, err := runMesh(t, "", "tags", "--device", "gw", "--set", "peer", "--apply"); err == nil {
		t.Fatal("malformed tag accepted")
	}
}

func Test_MeshAuthKeyAndOnboardWriteSecretFile(t *testing.T) {
	_, dir := setupMesh(t)
	keyFile := filepath.Join(dir, "authkey")
	out, err := runMesh(t, "", "authkey", "--tags", "tag:device", "--out", keyFile, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "FRESH-AUTH-KEY") {
		t.Fatalf("auth key printed: %s", out)
	}
	b, _ := os.ReadFile(keyFile)
	if strings.TrimSpace(string(b)) != "tskey-auth-FRESH-AUTH-KEY" {
		t.Fatalf("key file: %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(keyFile); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
	if _, err := runMesh(t, "", "authkey", "--out", keyFile); err == nil {
		t.Fatal("existing key file overwritten")
	}
	if out, err := runMesh(t, "", "onboard", "--authkey-file", filepath.Join(dir, "k2"), "--config", "xmesh.yaml"); err != nil || !strings.Contains(out, "xmesh install --config xmesh.yaml") || !strings.Contains(out, "mesh.tailnet.authkey_file") {
		t.Fatalf("onboard: %v\n%s", err, out)
	}
	if _, err := runMesh(t, "", "authkey", "--tags", "device", "--out", filepath.Join(dir, "k3")); err == nil {
		t.Fatal("bad tag accepted")
	}
}

// conformance: CTR-MESH-03
func Test_MeshMatrixUsesNodeConfig(t *testing.T) {
	f, dir := setupMesh(t)
	f.policy = `{"acls":[{"action":"accept","src":["*"],"dst":["tag:device:7433"]}]}`
	f.devices[1]["tags"] = []string{"tag:peer"}
	cfg := filepath.Join(dir, "box.yaml")
	_ = os.WriteFile(cfg, []byte("node:\n  name: box\n  roles: [agent]\nagent:\n  authz:\n    tag:ingress:\n      tools: [\"fs.*\"]\n"), 0o600)
	out, err := runMesh(t, "", "matrix", "--node-config", cfg, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var m meshadmin.Matrix
	if err := json.Unmarshal([]byte(out), &m); err != nil || len(m.Cells) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	if c := m.Cells[0]; c.Src != "gw" || c.Network != meshadmin.Allow || c.Tools != meshadmin.Deny || c.Mismatch == "" {
		t.Fatalf("expected a network/tool mismatch for gw->box: %+v", c)
	}
	if human, _ := runMesh(t, "", "matrix", "--node-config", cfg, "--mismatch-only"); !strings.Contains(human, "agent.authz") {
		t.Fatalf("human matrix:\n%s", human)
	}
}

// conformance: CON-MESH-02
func Test_MeshLoginStoresWithoutPrintingSecret(t *testing.T) {
	_, url := newCLITailscale(t)
	store := &meshadmin.MemoryStore{}
	old := meshCredentials
	meshCredentials = store
	defer func() { meshCredentials = old }()
	out, err := runMesh(t, cliSecret+"\n", "login", "--kind", "tailscale", "--url", url, "--access", "write", "--profile", "lab")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, cliSecret) {
		t.Fatalf("login echoed the secret: %s", out)
	}
	c, err := store.Get("lab", meshadmin.AccessWrite)
	if err != nil || c.Secret != cliSecret || c.Type != meshadmin.CredToken {
		t.Fatalf("stored credential: %v %+v", err, c)
	}
	// verification rejects a wrong secret and stores nothing
	if _, err := runMesh(t, "wrong\n", "login", "--kind", "tailscale", "--url", url, "--profile", "bad"); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if _, err := store.Get("bad", meshadmin.AccessRead); err == nil {
		t.Fatal("rejected credential was stored")
	}
	if _, err := runMesh(t, "", "logout", "--profile", "lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("lab", meshadmin.AccessWrite); err == nil {
		t.Fatal("logout kept the credential")
	}
	if _, err := runMesh(t, "x", "login", "--kind", "nope"); err == nil {
		t.Fatal("bad kind")
	}
}

// conformance: CTR-MESH-04
func Test_MeshDERPPlanAndHeadscaleFile(t *testing.T) {
	f, dir := setupMesh(t)
	out, err := runMesh(t, "", "derp", "plan", "--region", "code=lab,name=Lab,host=derp.example.com,ipv4=198.51.100.7", "--omit-default", "--json", "--snapshot-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `\"900\"`) || strings.Contains(f.policy, "derpMap") {
		t.Fatalf("plan must preview region 900 without writing:\n%s", out)
	}
	file := filepath.Join(dir, "derp.yaml")
	if out, err := runMesh(t, "", "derp", "headscale", "--region", "code=lab,name=Lab,host=derp.example.com,ipv4=198.51.100.7", "--omit-default", "--out", file); err != nil || !strings.Contains(out, "urls: []") {
		t.Fatalf("headscale: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "hostname: derp.example.com") {
		t.Fatalf("derp file: %s", b)
	}
	if _, err := runMesh(t, "", "derp", "plan", "--region", "code=lab,name=Lab,host=https://x"); err == nil {
		t.Fatal("bad host accepted")
	}
	if _, err := runMesh(t, "", "derp", "plan", "--region", "bogus"); err == nil {
		t.Fatal("malformed region accepted")
	}
}

// conformance: CTR-MESH-05
func Test_MeshDERPSyncAllowlist(t *testing.T) {
	setupMesh(t)
	list := filepath.Join(t.TempDir(), "allow.json")
	out, err := runMesh(t, "", "derp", "sync", "--allowlist", list, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var r meshadmin.SyncResult
	_ = json.Unmarshal([]byte(out), &r)
	if r.Total != 1 || r.Added != 1 { // only box carries an xmesh tag
		t.Fatalf("%+v", r)
	}
	al, _ := meshadmin.OpenAllowlist(list)
	if !al.Allowed("nodekey:" + strings.Repeat("ab", 32)) {
		t.Fatal("box not admitted")
	}
	if al.Allowed("nodekey:" + strings.Repeat("cd", 32)) {
		t.Fatal("untagged gw admitted")
	}
	if _, err := runMesh(t, "", "derp", "sync"); err == nil {
		t.Fatal("sync without target")
	}
}

func Test_MeshUsageAndUnknown(t *testing.T) {
	if out, err := runMesh(t, "", "help"); err != nil || !strings.Contains(out, "xmesh mesh") {
		t.Fatalf("%v", err)
	}
	if _, err := runMesh(t, "", "nope"); err == nil {
		t.Fatal("unknown subcommand")
	}
	if e := run([]string{"mesh", "help"}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(usage, "mesh ") {
		t.Fatal("main usage must list mesh")
	}
}

// A credential file replaces the keychain profile, so a headless host works
// through the same code path the desktop panel uses.
func Test_MeshLoginWritesCredentialFile(t *testing.T) {
	_, url := newCLITailscale(t)
	old := meshCredentials
	meshCredentials = &meshadmin.MemoryStore{}
	defer func() { meshCredentials = old }()
	dir := t.TempDir()
	file := filepath.Join(dir, "mesh", "mesh-credential.json")
	out, err := runMesh(t, cliSecret+"\n", "login", "--kind", "tailscale", "--url", url, "--access", "write", "--credential-file", file, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, cliSecret) {
		t.Fatalf("login echoed the secret: %s", out)
	}
	cred, err := meshadmin.ReadCredentialFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Secret != cliSecret || cred.Kind != meshadmin.KindTailscale {
		t.Fatalf("stored credential: %+v", cred)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("credential file mode %v", st.Mode().Perm())
		}
	}
	// Status reads the same file without consulting the keychain store.
	if _, err := runMesh(t, "", "status", "--json", "--credential-file", file); err != nil {
		t.Fatal(err)
	}
	if _, err := runMesh(t, "", "logout", "--credential-file", file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logout kept the credential file: %v", err)
	}
}

func TestMeshCredentialFileAccessAndMissingLogin(t *testing.T) {
	_, url := newCLITailscale(t)
	file := filepath.Join(t.TempDir(), "credential.json")
	if _, err := runMesh(t, "", "status", "--credential-file", file); err == nil || !strings.Contains(err.Error(), "没有保存的凭据") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := runMesh(t, cliSecret+"\n", "login", "--kind", "tailscale", "--url", url, "--access", "read", "--credential-file", file); err != nil {
		t.Fatal(err)
	}
	if _, err := runMesh(t, "", "authkey", "--tags", "tag:device", "--out", filepath.Join(t.TempDir(), "key"), "--credential-file", file); err == nil || !strings.Contains(err.Error(), "insufficient_scope") {
		t.Fatalf("read-only: %v", err)
	}
	out, err := runMesh(t, "", "status", "--json", "--credential-file", file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Credential struct {
			Access string `json:"access"`
			Source string `json:"source"`
		} `json:"credential"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Credential.Access != "read" || doc.Credential.Source != "file" {
		t.Fatalf("status: %s", out)
	}
}

func TestMeshPanelCredentialLocation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XMESH_UI_STATE_ROOT", root)
	c := meshCommon{panel: true}
	if err := c.resolveCredentialFile(); err != nil {
		t.Fatal(err)
	}
	if c.credFile != filepath.Join(root, "mesh", "mesh-credential.json") {
		t.Fatalf("panel credential path: %s", c.credFile)
	}
}

func TestMeshOnboardRefusesLegacyInstallBeforeCreatingKey(t *testing.T) {
	_, dir := setupMesh(t)
	cfg := filepath.Join(dir, "legacy.yaml")
	if err := os.WriteFile(cfg, []byte(cliConfig), 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "unused-key")
	if _, err := runMesh(t, "", "onboard", "--authkey-file", key, "--config", cfg, "--install"); err == nil {
		t.Fatal("legacy config installed")
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatal("key created before config preflight", err)
	}
}
