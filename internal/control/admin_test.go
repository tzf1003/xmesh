package control

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/mesh"
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

// conformance: CTR-CTL-08 pairing is export and import with the user's own fingerprint, lan mode only
func Test_ControlPairExportImport(t *testing.T) {
	f := newFixture(t)
	var exported v1.PairExportResponse
	decodeAs(t, f.ok("POST", "/v1/pair/export", nil, "PairExportResponse"), &exported)
	if exported.Pair.Name != "ctl-node" || len(exported.Pair.Tags) != 0 || len(exported.Pair.Fingerprint) != 64 {
		t.Fatalf("%+v", exported)
	}
	// the pairing document is the one the CLI would write, so the peer can import it
	other, err := mesh.LoadCertificate(t.TempDir(), "peer-b")
	if err != nil {
		t.Fatal(err)
	}
	peer := v1.PairDocument{Name: "peer-b", Fingerprint: other.Fingerprint, CertificatePEM: string(other.PEM), Tags: []string{}}
	colon := strings.ToUpper(other.Fingerprint[:2] + ":" + other.Fingerprint[2:])

	apiError(t, f.do("POST", "/v1/pair/import", v1.PairImportRequest{Pair: peer, Fingerprint: otherFingerprint(other.Fingerprint)}, nil), v1.ErrFingerprintMismatch)
	apiError(t, f.do("POST", "/v1/pair/import", v1.PairImportRequest{Pair: peer}, nil), v1.ErrFingerprintMismatch)
	apiError(t, f.do("POST", "/v1/pair/import", v1.PairImportRequest{Pair: peer, Fingerprint: other.Fingerprint, Tags: []string{"tag:admin"}}, nil), v1.ErrInvalidArgs)
	// a document whose own fingerprint field lies is refused even when the user confirmed the real one
	lying := peer
	lying.Fingerprint = strings.Repeat("a", 64)
	apiError(t, f.do("POST", "/v1/pair/import", v1.PairImportRequest{Pair: lying, Fingerprint: other.Fingerprint}, nil), v1.ErrFingerprintMismatch)
	if _, err = os.Stat(filepath.Join(f.server.o.StateDir, "lan", "peers.json")); err == nil {
		t.Fatal("a refused import left a trust record")
	}

	if st := f.server.snapshot(); st.RestartRequired {
		t.Fatalf("restart required before any import: %+v", st.Warnings)
	}
	var imported v1.PairImportResponse
	decodeAs(t, f.ok("POST", "/v1/pair/import", v1.PairImportRequest{Pair: peer, Fingerprint: colon}, "PairImportResponse"), &imported)
	// the trust store is read at start, so the status (and the tray icon built from it) says restart
	if st := f.server.snapshot(); !st.RestartRequired || !slices.ContainsFunc(st.Warnings, func(w v1.Warning) bool { return w.Code == v1.WarnRestartRequired }) {
		t.Fatalf("an imported peer left no restart notice: %+v", st)
	}
	if imported.Name != "peer-b" || imported.Fingerprint != other.Fingerprint || !imported.RestartRequired || strings.Join(imported.Tags, ",") != "tag:peer,tag:device" {
		t.Fatalf("%+v", imported)
	}
	store, err := mesh.OpenTrust(filepath.Join(f.server.o.StateDir, "lan"))
	if err != nil {
		t.Fatal(err)
	}
	if trusted, ok := store.Find(other.Fingerprint); !ok || trusted.Name != "peer-b" || len(trusted.Tags) != 2 {
		t.Fatalf("%+v %v", trusted, ok)
	}
	f.ok("POST", "/v1/pair/import", v1.PairImportRequest{Pair: peer, Fingerprint: other.Fingerprint, Tags: []string{"tag:ingress", "tag:ingress"}}, "PairImportResponse")
}

func Test_ControlPairNeedsLANMode(t *testing.T) {
	f := newFixtureConfig(t, tailnetConfig)
	apiError(t, f.do("POST", "/v1/pair/export", nil, nil), v1.ErrUnsupportedMode)
	apiError(t, f.do("POST", "/v1/pair/import", v1.PairImportRequest{}, nil), v1.ErrUnsupportedMode)
}

// conformance: CTR-CTL-09 PAT lifecycle: the token appears once, in the response that creates or rotates it
func Test_ControlPats(t *testing.T) {
	bare := newFixture(t)
	if e := apiError(t, bare.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil), v1.ErrConflict); e.Reason != "gateway_not_configured" {
		t.Fatalf("%+v", e)
	}
	bare.server.Close() // the pipe name is global on Windows: one server per node at a time
	f := newFixtureConfig(t, gatewayConfig)
	var none v1.PatList
	decodeAs(t, f.ok("GET", "/v1/pats", nil, "PatList"), &none)
	if none.Enforced || len(none.Pats) != 0 {
		t.Fatalf("%+v", none)
	}
	events := f.stream("")
	apiError(t, f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "../x", Scopes: []string{"devices:read"}}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci", Scopes: []string{"bogus"}}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci"}, nil), v1.ErrInvalidArgs)

	r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read", "exec:*"}}, nil)
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	valid(t, "PatSecret", r.Body)
	var created v1.PatSecret
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
	apiError(t, f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil), v1.ErrConflict)

	// the configuration now lists it, and the event stream says the channel wrote it
	if cfg := f.store.Get().Gateway.PATs["ci"]; cfg.TokenFile != file || len(cfg.Scopes) != 2 {
		t.Fatalf("%+v", cfg)
	}
	var written v1.ConfigEventData
	payload(t, events.next(t, ofType(v1.EventConfig)), &written)
	if written.Actor != v1.ActorControl || len(written.ChangedPaths) == 0 || !strings.HasPrefix(written.ChangedPaths[0], "/gateway") {
		t.Fatalf("%+v", written)
	}

	list := f.ok("GET", "/v1/pats", nil, "PatList")
	var listed v1.PatList
	decodeAs(t, list, &listed)
	if !listed.Enforced || len(listed.Pats) != 1 || listed.Pats[0].Name != "ci" || strings.Contains(string(list.Body), created.Token) {
		t.Fatalf("%s", list.Body)
	}

	rotated := f.ok("POST", "/v1/pats/ci/rotate", nil, "PatSecret")
	var second v1.PatSecret
	decodeAs(t, rotated, &second)
	if second.Token == created.Token || len(second.Token) < 32 {
		t.Fatal("rotation kept the old token")
	}
	if got, _ := os.ReadFile(file); strings.TrimSpace(string(got)) != second.Token {
		t.Fatal("token file not replaced")
	}
	apiError(t, f.do("POST", "/v1/pats/missing/rotate", nil, nil), v1.ErrNotFound)

	// no other response carries a token
	for _, path := range []string{"/v1/config", "/v1/status", "/v1/pats"} {
		if body := f.do("GET", path, nil, nil).Body; strings.Contains(string(body), second.Token) || strings.Contains(string(body), created.Token) {
			t.Fatalf("%s leaks a token", path)
		}
	}

	// The gateway always needs a token, so the last PAT cannot be deleted (CTR-CTL-09).
	if r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r = f.do("DELETE", "/v1/pats/ci", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	apiError(t, f.do("DELETE", "/v1/pats/spare", nil, nil), v1.ErrConflict)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("token file kept")
	}
	if _, ok := f.store.Get().Gateway.PATs["ci"]; ok {
		t.Fatal("entry kept")
	}
	if raw, _ := os.ReadFile(f.path); strings.Contains(string(raw), "    ci:") {
		t.Fatalf("entry left behind:\n%s", raw)
	}
	apiError(t, f.do("DELETE", "/v1/pats/ci", nil, nil), v1.ErrNotFound)
	var after v1.PatList
	decodeAs(t, f.ok("GET", "/v1/pats", nil, "PatList"), &after)
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
	f.ok("POST", "/v1/pats/own/rotate", nil, "PatSecret")
	if got, _ := os.ReadFile(foreign); strings.TrimSpace(string(got)) == strings.Repeat("a", 40) {
		t.Fatal("not rotated")
	}
	if r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := f.do("DELETE", "/v1/pats/own", nil, nil); r.Status != 204 {
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

func (g *fakeIngress) Issuer() string { return "https://x.example.com" }
func (g *fakeIngress) Clients() []v1.IngressClient {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []v1.IngressClient{}
	for id := range g.clients {
		out = append(out, v1.IngressClient{ClientID: id, ClientName: "Claude", RedirectURIs: []string{"https://claude.ai/cb"}, Scopes: []string{"devices:read"}, TokenEndpointAuthMethod: "none"})
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
	apiError(t, plain.do("GET", "/v1/ingress/clients", nil, nil), v1.ErrRoleDisabled)
	apiError(t, plain.do("DELETE", "/v1/ingress/clients/abc", nil, nil), v1.ErrRoleDisabled)
	apiError(t, plain.do("PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: "a long enough password"}, nil), v1.ErrRoleDisabled)

	plain.server.Close() // the pipe name is global on Windows: one server per node at a time
	notReady := newFixtureConfig(t, tailnetIngressConfig)
	apiError(t, notReady.do("GET", "/v1/ingress/clients", nil, nil), v1.ErrUnavailable)
	notReady.server.Close()

	admin := &fakeIngress{clients: map[string]bool{"c1": true}}
	f := newFixtureConfig(t, tailnetIngressConfig, func(o *Options) { o.Ingress = admin })
	var listed v1.IngressClientsResponse
	decodeAs(t, f.ok("GET", "/v1/ingress/clients", nil, "IngressClientsResponse"), &listed)
	if listed.Issuer == nil || *listed.Issuer != "https://x.example.com" || len(listed.Clients) != 1 || listed.Clients[0].ClientID != "c1" {
		t.Fatalf("%+v", listed)
	}
	if r := f.do("DELETE", "/v1/ingress/clients/c1", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	apiError(t, f.do("DELETE", "/v1/ingress/clients/c1", nil, nil), v1.ErrNotFound)
	decodeAs(t, f.ok("GET", "/v1/ingress/clients", nil, "IngressClientsResponse"), &listed)
	if len(listed.Clients) != 0 {
		t.Fatalf("%+v", listed)
	}

	apiError(t, f.do("PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: "short"}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: " padded password "}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: strings.Repeat("x", 257)}, nil), v1.ErrInvalidArgs)
	secret := "correct horse battery staple"
	r := f.ok("PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: secret}, "IngressPasswordResponse")
	if admin.password != secret || strings.Contains(string(r.Body), "horse") {
		t.Fatalf("%q %s", admin.password, r.Body)
	}
	admin.failWith = &PasswordSourceError{}
	if e := apiError(t, f.do("PUT", "/v1/ingress/password", v1.IngressPasswordRequest{Password: secret + "2"}, nil), v1.ErrConflict); e.Reason != "password_source_not_file" {
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
		apiError(t, f.do("POST", "/v1/pats/"+name+"/rotate", nil, nil), v1.ErrInvalidArgs)
		apiError(t, f.do("DELETE", "/v1/pats/"+name, nil, nil), v1.ErrInvalidArgs)
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
	r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil)
	if r.Status != 201 {
		t.Fatal(r.Status)
	}
	var secret v1.PatSecret
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
	if r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "spare", Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
		t.Fatalf("create spare: %d", r.Status)
	}
	if r := f.do("DELETE", "/v1/pats/default", nil, nil); r.Status != 204 {
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
		if r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: name, Scopes: []string{"devices:read"}}, nil); r.Status != 201 {
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
	apiError(t, f.do("DELETE", "/v1/pats/ci", nil, nil), v1.ErrInternal)
	if _, ok := f.store.Get().Gateway.PATs["ci"]; ok {
		t.Fatal("failed cleanup did not revoke PAT")
	}
	r := f.do("POST", "/v1/pats", v1.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, nil)
	if r.Status != 201 {
		t.Fatal("recreation blocked", r.Status)
	}
	var secret v1.PatSecret
	decodeAs(t, r, &secret)
	if secret.Pat.TokenFile == file {
		t.Fatal("retained file reused")
	}
}
