package control

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
)

const gatewayConfig = testConfig + `gateway:
  listen: 127.0.0.1:8099
`

const tailnetConfig = `node:
  name: ctl-node
  roles: [agent]
mesh:
  mode: tailnet
  control_url: https://controlplane.tailscale.com
  authkey_file: /nonexistent/authkey
agent:
  default_cwd: .
  cwd_policy:
    allow: ["."]
`

const tailnetIngressConfig = `node:
  name: ctl-node
  roles: [agent, ingress]
mesh:
  mode: tailnet
  control_url: https://controlplane.tailscale.com
  authkey_file: /nonexistent/authkey
ingress:
  listen: tunnel
  oauth:
    issuer: https://x.example.com
agent:
  default_cwd: .
  cwd_policy:
    allow: ["."]
`

func pairingDocument(t *testing.T, name string) v2.PairDocument {
	t.Helper()
	id, err := device.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	doc := id.Document(name)
	return v2.PairDocument{Version: 2, Name: name, DeviceID: doc.DeviceID, PublicKey: doc.PublicKey, Fingerprint: strings.TrimPrefix(doc.DeviceID, "dev-"), Tags: []string{}}
}

// conformance: CTR-CTL-08 trust follows the paired public identity and applies immediately.
func Test_ControlPairExportImport(t *testing.T) {
	f := newFixture(t)
	var exported v2.PairExportResponse
	decodeAs(t, f.ok("POST", "/v2/pair/export", nil, "PairExportResponse"), &exported)
	if exported.Pair.Version != 2 || exported.Pair.Name != "ctl-node" || exported.Pair.DeviceID != "dev-"+exported.Pair.Fingerprint || len(exported.Pair.PublicKey) == 0 {
		t.Fatal(exported)
	}
	peer := pairingDocument(t, "peer-b")
	colon := strings.ToUpper(peer.Fingerprint[:2] + ":" + peer.Fingerprint[2:])
	apiError(t, f.do("POST", "/v2/pair/import", v2.PairImportRequest{Pair: peer, Fingerprint: otherFingerprint(peer.Fingerprint)}, nil), v2.ErrFingerprintMismatch)
	apiError(t, f.do("POST", "/v2/pair/import", v2.PairImportRequest{Pair: peer}, nil), v2.ErrFingerprintMismatch)
	apiError(t, f.do("POST", "/v2/pair/import", v2.PairImportRequest{Pair: peer, Fingerprint: peer.Fingerprint, Tags: []string{"tag:unknown"}}, nil), v2.ErrInvalidArgs)
	lying := peer
	lying.Fingerprint = strings.Repeat("a", 64)
	apiError(t, f.do("POST", "/v2/pair/import", v2.PairImportRequest{Pair: lying, Fingerprint: peer.Fingerprint}, nil), v2.ErrFingerprintMismatch)
	if _, err := os.Stat(filepath.Join(f.server.o.StateDir, "identity-v2", "trust.json")); err == nil {
		t.Fatal("refused import persisted trust")
	}
	var imported v2.PairImportResponse
	decodeAs(t, f.ok("POST", "/v2/pair/import", v2.PairImportRequest{Pair: peer, Fingerprint: colon}, "PairImportResponse"), &imported)
	if imported.Name != "peer-b" || imported.Fingerprint != peer.Fingerprint || imported.RestartRequired || strings.Join(imported.Tags, ",") != "tag:peer,tag:device" {
		t.Fatal(imported)
	}
	if st := f.server.snapshot(); st.RestartRequired {
		t.Fatal("shared device trust required restart", st.Warnings)
	}
	store, err := device.OpenTrust(f.server.o.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if trusted, ok := store.Find(peer.DeviceID); !ok || trusted.Document.Name != "peer-b" || len(trusted.Tags) != 2 {
		t.Fatal(trusted, ok)
	}
	f.ok("POST", "/v2/pair/import", v2.PairImportRequest{Pair: peer, Fingerprint: peer.Fingerprint, Tags: []string{"tag:ingress", "tag:ingress"}}, "PairImportResponse")
}

func Test_ControlPublicPairingIdentityIndependentOfNetworkMode(t *testing.T) {
	f := newFixtureConfig(t, tailnetConfig)
	var exported v2.PairExportResponse
	decodeAs(t, f.ok("POST", "/v2/pair/export", nil, "PairExportResponse"), &exported)
	peer := pairingDocument(t, "peer")
	f.ok("POST", "/v2/pair/import", v2.PairImportRequest{Pair: peer, Fingerprint: peer.Fingerprint}, "PairImportResponse")
}

// conformance: CTR-CTL-09 PAT lifecycle: the token appears once, in the response that creates or rotates it
func Test_ControlPats(t *testing.T) {
	bare := newFixture(t)
	if e := apiError(t, bare.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil), v2.ErrConflict); e.Reason != "gateway_not_configured" {
		t.Fatalf("%+v", e)
	}
	bare.server.Close() // the pipe name is global on Windows: one server per node at a time
	f := newFixtureConfig(t, gatewayConfig)
	var none v2.PatList
	decodeAs(t, f.ok("GET", "/v2/pats", nil, "PatList"), &none)
	if none.Enforced || len(none.Pats) != 0 {
		t.Fatalf("%+v", none)
	}
	events := f.stream("")
	apiError(t, f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "../x", Scopes: []string{"devices:read"}}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"bogus"}}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci"}, nil), v2.ErrInvalidArgs)

	r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read", "exec:*"}}, nil)
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	valid(t, "PatSecret", r.Body)
	var created v2.PatSecret
	decodeAs(t, r, &created)
	file := filepath.Join(f.server.o.StateDir, "pats", "ci.token")
	if len(created.Token) < 32 || created.Pat.Name != "ci" || created.Pat.TokenFile != file || created.Pat.ModifiedAt == nil {
		t.Fatalf("%+v", created)
	}
	if got, err := os.ReadFile(file); err != nil || strings.TrimSpace(string(got)) != created.Token {
		t.Fatalf("token file: %v %q", err, got)
	}
	if st, _ := os.Stat(file); runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf("token file mode %v", st.Mode())
	}
	apiError(t, f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil), v2.ErrConflict)

	// the configuration now lists it, and the event stream says the channel wrote it
	if cfg := f.store.Get().Gateway.PATs["ci"]; cfg.TokenFile != file || len(cfg.Scopes) != 2 {
		t.Fatalf("%+v", cfg)
	}
	var written v2.ConfigEventData
	payload(t, events.next(t, ofType(v2.EventConfig)), &written)
	if written.Actor != v2.ActorControl || len(written.ChangedPaths) == 0 || !strings.HasPrefix(written.ChangedPaths[0], "/gateway") {
		t.Fatalf("%+v", written)
	}

	list := f.ok("GET", "/v2/pats", nil, "PatList")
	var listed v2.PatList
	decodeAs(t, list, &listed)
	if !listed.Enforced || len(listed.Pats) != 1 || listed.Pats[0].Name != "ci" || strings.Contains(string(list.Body), created.Token) {
		t.Fatalf("%s", list.Body)
	}

	rotated := f.ok("POST", "/v2/pats/ci/rotate", nil, "PatSecret")
	var second v2.PatSecret
	decodeAs(t, rotated, &second)
	if second.Token == created.Token || len(second.Token) < 32 {
		t.Fatal("rotation kept the old token")
	}
	if got, _ := os.ReadFile(file); strings.TrimSpace(string(got)) != second.Token {
		t.Fatal("token file not replaced")
	}
	apiError(t, f.do("POST", "/v2/pats/missing/rotate", nil, nil), v2.ErrNotFound)

	// no other response carries a token
	for _, path := range []string{"/v2/config", "/v2/status", "/v2/pats"} {
		if body := f.do("GET", path, nil, nil).Body; strings.Contains(string(body), second.Token) || strings.Contains(string(body), created.Token) {
			t.Fatalf("%s leaks a token", path)
		}
	}

	// The gateway always needs a token, so the last PAT cannot be deleted (CTR-CTL-09).
	if r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r = f.do("DELETE", "/v2/pats/ci", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	apiError(t, f.do("DELETE", "/v2/pats/spare", nil, nil), v2.ErrConflict)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("token file kept")
	}
	if _, ok := f.store.Get().Gateway.PATs["ci"]; ok {
		t.Fatal("entry kept")
	}
	if raw, _ := os.ReadFile(f.path); strings.Contains(string(raw), "    ci:") {
		t.Fatalf("entry left behind:\n%s", raw)
	}
	apiError(t, f.do("DELETE", "/v2/pats/ci", nil, nil), v2.ErrNotFound)
	var after v2.PatList
	decodeAs(t, f.ok("GET", "/v2/pats", nil, "PatList"), &after)
	if !after.Enforced || len(after.Pats) != 1 || after.Pats[0].Name != "spare" {
		t.Fatalf("%+v", after)
	}
}

// A token file the operator pointed at from the configuration is rotated in place but never deleted.
func Test_ControlPatsKeepForeignTokenFile(t *testing.T) {
	f := newFixtureConfig(t, gatewayConfig)
	foreign := filepath.Join(f.server.patDir(), "own.token")
	if err := os.MkdirAll(filepath.Dir(foreign), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig + "gateway:\n  listen: 127.0.0.1:8099\n  pats:\n    own:\n      token_file: " + foreign + "\n      scopes: [\"devices:read\"]\n"
	if err := os.WriteFile(f.path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	f.store.Get()
	f.ok("POST", "/v2/pats/own/rotate", nil, "PatSecret")
	if got, _ := os.ReadFile(foreign); strings.TrimSpace(string(got)) == strings.Repeat("a", 40) {
		t.Fatal("not rotated")
	}
	if r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := f.do("DELETE", "/v2/pats/own", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("a token file outside the state directory was deleted")
	}
}

type fakeIngress struct {
	mu       sync.Mutex
	clients  map[string]bool
	password string
	failWith error
}

func Test_ControlPatsReportAbsoluteTokenPath(t *testing.T) {
	t.Chdir(t.TempDir())
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workingDir, err = filepath.EvalSymlinks(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(workingDir, "operator.token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(workingDir, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	// An inherited PWD may name a different directory than the process actually uses.
	t.Setenv("PWD", t.TempDir())
	f := newFixtureConfig(t, gatewayConfig+"  pats:\n    operator:\n      token_file: "+relative+"\n      scopes: [\"devices:read\"]\n")
	var listed v2.PatList
	decodeAs(t, f.ok("GET", "/v2/pats", nil, "PatList"), &listed)
	if len(listed.Pats) != 1 || listed.Pats[0].TokenFile != tokenFile || listed.Pats[0].ModifiedAt == nil {
		t.Fatalf("unexpected token path or modification time: %+v", listed.Pats)
	}
	if got := f.store.Get().Gateway.PATs["operator"].TokenFile; got != relative {
		t.Fatalf("configuration token path changed: %q", got)
	}
	if got := patInfo("missing", config.PAT{TokenFile: "missing.token"}).TokenFile; got != filepath.Join(workingDir, "missing.token") {
		t.Fatalf("missing token path = %q", got)
	}
}

func (g *fakeIngress) Issuer() string { return "https://x.example.com" }
func (g *fakeIngress) Clients() []v2.IngressClient {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []v2.IngressClient{}
	for id := range g.clients {
		out = append(out, v2.IngressClient{ClientID: id, ClientName: "Claude", RedirectURIs: []string{"https://claude.ai/cb"}, Scopes: []string{"devices:read"}, TokenEndpointAuthMethod: "none"})
	}
	return out
}
func (g *fakeIngress) RevokeClient(id string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.clients[id] {
		return false, nil
	}
	delete(g.clients, id)
	return true, nil
}
func (g *fakeIngress) RotatePassword(p string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failWith != nil {
		return g.failWith
	}
	g.password = p
	return nil
}

// conformance: CTR-CTL-10 ingress administration needs the role; the password is write-only
func Test_ControlIngress(t *testing.T) {
	plain := newFixture(t)
	apiError(t, plain.do("GET", "/v2/ingress/clients", nil, nil), v2.ErrRoleDisabled)
	apiError(t, plain.do("DELETE", "/v2/ingress/clients/abc", nil, nil), v2.ErrRoleDisabled)
	apiError(t, plain.do("PUT", "/v2/ingress/password", v2.IngressPasswordRequest{Password: "a long enough password"}, nil), v2.ErrRoleDisabled)

	plain.server.Close() // the pipe name is global on Windows: one server per node at a time
	notReady := newFixtureConfig(t, tailnetIngressConfig)
	apiError(t, notReady.do("GET", "/v2/ingress/clients", nil, nil), v2.ErrUnavailable)
	notReady.server.Close()

	admin := &fakeIngress{clients: map[string]bool{"c1": true}}
	f := newFixtureConfig(t, tailnetIngressConfig, func(o *Options) { o.Ingress = admin })
	var listed v2.IngressClientsResponse
	decodeAs(t, f.ok("GET", "/v2/ingress/clients", nil, "IngressClientsResponse"), &listed)
	if listed.Issuer == nil || *listed.Issuer != "https://x.example.com" || len(listed.Clients) != 1 || listed.Clients[0].ClientID != "c1" {
		t.Fatalf("%+v", listed)
	}
	if r := f.do("DELETE", "/v2/ingress/clients/c1", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	apiError(t, f.do("DELETE", "/v2/ingress/clients/c1", nil, nil), v2.ErrNotFound)
	decodeAs(t, f.ok("GET", "/v2/ingress/clients", nil, "IngressClientsResponse"), &listed)
	if len(listed.Clients) != 0 {
		t.Fatalf("%+v", listed)
	}

	apiError(t, f.do("PUT", "/v2/ingress/password", v2.IngressPasswordRequest{Password: "short"}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("PUT", "/v2/ingress/password", v2.IngressPasswordRequest{Password: " padded password "}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("PUT", "/v2/ingress/password", v2.IngressPasswordRequest{Password: strings.Repeat("x", 257)}, nil), v2.ErrInvalidArgs)
	secret := "correct horse battery staple"
	r := f.ok("PUT", "/v2/ingress/password", v2.IngressPasswordRequest{Password: secret}, "IngressPasswordResponse")
	if admin.password != secret || strings.Contains(string(r.Body), "horse") {
		t.Fatalf("%q %s", admin.password, r.Body)
	}
	admin.failWith = &PasswordSourceError{}
	if e := apiError(t, f.do("PUT", "/v2/ingress/password", v2.IngressPasswordRequest{Password: secret + "2"}, nil), v2.ErrConflict); e.Reason != "password_source_not_file" {
		t.Fatalf("%+v", e)
	}
}

// otherFingerprint returns a well-formed fingerprint that differs from fp. Swapping in a fixed
// prefix would sometimes reproduce fp itself: one certificate in 256 starts with those bytes.
func otherFingerprint(fp string) string {
	if fp[0] == '0' {
		return "1" + fp[1:]
	}
	return "0" + fp[1:]
}

func Test_ControlPatsRejectPointerNames(t *testing.T) {
	f := newFixtureConfig(t, gatewayConfig)
	for _, name := range []string{"~1", "~0", "bad.name"} {
		apiError(t, f.do("POST", "/v2/pats/"+name+"/rotate", nil, nil), v2.ErrInvalidArgs)
		apiError(t, f.do("DELETE", "/v2/pats/"+name, nil, nil), v2.ErrInvalidArgs)
	}
}

func Test_ControlPatCreationPreservesUnconfiguredCredential(t *testing.T) {
	f := newFixtureConfig(t, gatewayConfig)
	if err := os.MkdirAll(f.server.patDir(), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.server.patDir(), "ci.token")
	if err := os.WriteFile(file, []byte("operator-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil)
	if r.Status != 201 {
		t.Fatal(r.Status)
	}
	var secret v2.PatSecret
	decodeAs(t, r, &secret)
	if secret.Pat.TokenFile == file {
		t.Fatal("operator file reused")
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != "operator-owned" {
		t.Fatalf("%q %v", body, err)
	}
}

func Test_ControlDefaultPatHasOwnershipReceipt(t *testing.T) {
	f := newFixtureConfig(t, gatewayTokenConfig(""))
	created, err := EnsureDefaultPAT(f.store, f.server.o.StateDir)
	if err != nil || !created {
		t.Fatalf("create default: %v %v", created, err)
	}
	file := filepath.Join(f.server.patDir(), "default.token")
	if raw, err := os.ReadFile(file + ".owner"); err != nil || string(raw) != "default\n" {
		t.Fatalf("receipt %q %v", raw, err)
	}
	if r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
		t.Fatalf("create spare: %d", r.Status)
	}
	if r := f.do("DELETE", "/v2/pats/default", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	for _, name := range []string{file, file + ".owner"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("managed default remained: %s %v", name, err)
		}
	}
}

func Test_ControlPatRecreationRecoversFailedCleanup(t *testing.T) {
	f := newFixtureConfig(t, gatewayConfig)
	for _, name := range []string{"ci", "spare"} {
		if r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: name, Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
			t.Fatal(r.Status)
		}
	}
	file := filepath.Join(f.server.patDir(), "ci.token")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(file, "busy"), []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	apiError(t, f.do("DELETE", "/v2/pats/ci", nil, nil), v2.ErrInternal)
	if _, ok := f.store.Get().Gateway.PATs["ci"]; ok {
		t.Fatal("failed cleanup did not revoke PAT")
	}
	r := f.do("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil)
	if r.Status != 201 {
		t.Fatal("recreation blocked", r.Status)
	}
	var secret v2.PatSecret
	decodeAs(t, r, &secret)
	if secret.Pat.TokenFile == file {
		t.Fatal("retained file reused")
	}
}
