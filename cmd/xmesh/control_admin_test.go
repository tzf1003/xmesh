package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/device"
)

const cliGatewayConfig = cliConfig + "gateway:\n  listen: 127.0.0.1:8099\n"

const cliIngressConfig = `node:
  name: cli-node
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

// run executes `xmesh control <args> --state-dir state` and returns what it printed.
func runControl(t *testing.T, state string, args ...string) (string, error) {
	t.Helper()
	return output(t, func() error { return controlCommand(append(args, "--state-dir", state)) })
}

func runObject(t *testing.T, state string, args ...string) map[string]any {
	t.Helper()
	out, err := runControl(t, state, args...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	var v map[string]any
	if err = json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v printed %q", args, out)
	}
	return v
}

func cliPairDocument(t *testing.T, name string) v2.PairDocument {
	t.Helper()
	id, err := device.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	doc := id.Document(name)
	return v2.PairDocument{Version: 2, Name: name, DeviceID: doc.DeviceID, PublicKey: doc.PublicKey, Fingerprint: strings.TrimPrefix(doc.DeviceID, "dev-"), Tags: []string{}}
}
func Test_CLIControlPair(t *testing.T) {
	state, _ := startKernelStub(t)
	other := cliPairDocument(t, "peer-b")
	dir := t.TempDir()
	exported := runObject(t, state, "pair", "export")["pair"].(map[string]any)
	if exported["name"] != "cli-node" || len(exported["fingerprint"].(string)) != 64 {
		t.Fatal(exported)
	}
	file := filepath.Join(dir, "cli-node.pair.json")
	written := runObject(t, state, "pair", "export", "--out", file)
	if written["file"] != file || written["fingerprint"] != exported["fingerprint"] {
		t.Fatal(written)
	}
	var onDisk v2.PairDocument
	raw, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(raw, &onDisk) != nil || len(onDisk.PublicKey) == 0 || onDisk.DeviceID != "dev-"+onDisk.Fingerprint {
		t.Fatal(err, string(raw))
	}
	peerFile := filepath.Join(dir, "peer-b.json")
	doc, _ := json.Marshal(other)
	if err := os.WriteFile(peerFile, doc, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runControl(t, state, "pair", "import", "--file", peerFile, "--fingerprint", differentFingerprint(other.Fingerprint)); err == nil {
		t.Fatal("wrong fingerprint accepted")
	}
	if _, err := runControl(t, state, "pair", "import", "--file", peerFile); err == nil || !strings.Contains(err.Error(), "--fingerprint") {
		t.Fatal(err)
	}
	colon := strings.ToUpper(other.Fingerprint[:2] + ":" + other.Fingerprint[2:])
	imported := runObject(t, state, "pair", "import", "--file", peerFile, "--fingerprint", colon, "--tags", "tag:peer")
	if imported["name"] != "peer-b" || imported["fingerprint"] != other.Fingerprint || imported["restart_required"] != false {
		t.Fatal(imported)
	}
	third := cliPairDocument(t, "peer-c")
	wrapped, _ := json.Marshal(v2.PairExportResponse{Pair: third})
	if err := os.WriteFile(peerFile, wrapped, 0600); err != nil {
		t.Fatal(err)
	}
	if got := runObject(t, state, "pair", "import", "--file", peerFile, "--fingerprint", third.Fingerprint); got["name"] != "peer-c" {
		t.Fatal(got)
	}
}

func Test_CLIControlPats(t *testing.T) {
	state, configPath := startKernelStubWith(t, cliGatewayConfig, nil)
	if list := runObject(t, state, "pats", "list"); list["enforced"] != false || len(list["pats"].([]any)) != 0 {
		t.Fatal(list)
	}
	created := runObject(t, state, "pats", "create", "--name", "ci", "--scope", "devices:read,exec:box", "--scope", "fs:read:*")
	token := created["token"].(string)
	pat := created["pat"].(map[string]any)
	if len(token) < 32 || pat["name"] != "ci" || len(pat["scopes"].([]any)) != 3 {
		t.Fatal(created)
	}
	if body, _ := os.ReadFile(configPath); !strings.Contains(string(body), "ci:") {
		t.Fatalf("config has no entry:\n%s", body)
	}
	// list never carries the token
	out, err := runControl(t, state, "pats", "list")
	if err != nil || strings.Contains(out, token) || !strings.Contains(out, `"enforced": true`) {
		t.Fatalf("%v %s", err, out)
	}

	if _, err = runControl(t, state, "pats", "create", "--name", "ci", "--scope", "devices:read"); err == nil || !strings.Contains(err.Error(), "conflict") && !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range [][]string{{"pats"}, {"pats", "create", "--name", "x"}, {"pats", "create", "--scope", "devices:read"}, {"pats", "create", "--name", "bad name", "--scope", "devices:read"}, {"pats", "rotate"}, {"pats", "rotate", "nobody"}, {"pats", "delete", "nobody"}, {"pats", "list", "extra"}, {"pats", "frob"}} {
		if _, err = runControl(t, state, bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}

	// the name may come first or after --name; the new token replaces the old file content
	rotated := runObject(t, state, "pats", "rotate", "ci")
	fresh := rotated["token"].(string)
	if fresh == token || len(fresh) < 32 {
		t.Fatal(rotated)
	}
	file := rotated["pat"].(map[string]any)["token_file"].(string)
	if body, _ := os.ReadFile(file); strings.TrimSpace(string(body)) != fresh {
		t.Fatalf("token file %q", body)
	}
	if again := runObject(t, state, "pats", "rotate", "--name", "ci"); again["token"] == fresh {
		t.Fatal("rotation repeated a token")
	}

	// The last PAT cannot be deleted: the gateway always needs one (CTR-CTL-09).
	runObject(t, state, "pats", "create", "--name", "spare", "--scope", "devices:read")
	if gone := runObject(t, state, "pats", "delete", "ci"); gone["deleted"] != true || gone["name"] != "ci" {
		t.Fatal(gone)
	}
	if _, err = os.Stat(file); err == nil {
		t.Fatal("token file survived delete")
	}
	if list := runObject(t, state, "pats", "list"); list["enforced"] != true || len(list["pats"].([]any)) != 1 {
		t.Fatal(list)
	}
}

func Test_CLIControlPatsNeedGateway(t *testing.T) {
	state, _ := startKernelStub(t)
	if _, err := runControl(t, state, "pats", "create", "--name", "ci", "--scope", "devices:read"); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("%v", err)
	}
}

type cliIngress struct {
	clients  []v2.IngressClient
	password string
}

func (i *cliIngress) Issuer() string                { return "https://x.example.com" }
func (i *cliIngress) Clients() []v2.IngressClient   { return i.clients }
func (i *cliIngress) RotatePassword(p string) error { i.password = p; return nil }
func (i *cliIngress) RevokeClient(id string) (bool, error) {
	for n, c := range i.clients {
		if c.ClientID == id {
			i.clients = append(i.clients[:n], i.clients[n+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func Test_CLIControlIngress(t *testing.T) {
	admin := &cliIngress{clients: []v2.IngressClient{{ClientID: "c1", ClientName: "Claude", RedirectURIs: []string{"https://claude.ai/cb"}, Scopes: []string{"devices:read"}, TokenEndpointAuthMethod: "none"}}}
	state, _ := startKernelStubWith(t, cliIngressConfig, admin)
	listed := runObject(t, state, "ingress", "clients")
	if listed["issuer"] != "https://x.example.com" || len(listed["clients"].([]any)) != 1 {
		t.Fatal(listed)
	}
	if _, err := runControl(t, state, "ingress", "revoke", "nobody"); err == nil || !strings.Contains(err.Error(), "没有这个客户端") {
		t.Fatalf("unknown client: %v", err)
	}
	if got := runObject(t, state, "ingress", "revoke", "c1"); got["revoked"] != true || got["client_id"] != "c1" {
		t.Fatal(got)
	}
	if len(admin.clients) != 0 {
		t.Fatal("client survived revoke")
	}

	// the password comes from stdin or a file, never argv
	controlStdin = strings.NewReader("a long enough password\n")
	t.Cleanup(func() { controlStdin = os.Stdin })
	done := runObject(t, state, "ingress", "password")
	if done["rotated_at"] == nil || admin.password != "a long enough password" {
		t.Fatalf("%v %q", done, admin.password)
	}
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("another long password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runObject(t, state, "ingress", "password", "--password-file", pwFile)
	if admin.password != "another long password" {
		t.Fatalf("%q", admin.password)
	}
	if out, err := runControl(t, state, "ingress", "password", "a long enough password"); err == nil || strings.Contains(out, "enough") {
		t.Fatalf("password on the command line was accepted: %v", err)
	}
	controlStdin = strings.NewReader("short\n")
	if _, err := runControl(t, state, "ingress", "password"); err == nil {
		t.Fatal("a short password was accepted")
	}
	for _, bad := range [][]string{{"ingress"}, {"ingress", "revoke"}, {"ingress", "frob"}} {
		if _, err := runControl(t, state, bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func Test_CLIControlIngressNeedsRole(t *testing.T) {
	state, _ := startKernelStub(t)
	if _, err := runControl(t, state, "ingress", "clients"); err == nil || !strings.Contains(err.Error(), "ingress") {
		t.Fatalf("%v", err)
	}
}

// differentFingerprint changes the first hex digit, so the result never equals
// the input (swapping in a fixed prefix does for one fingerprint in 256).
func differentFingerprint(fingerprint string) string {
	first := byte('0')
	if fingerprint[0] == first {
		first = '1'
	}
	return string(first) + fingerprint[1:]
}

// cliPairing is the LAN pairing the CLI talks to: it remembers the password it was given and
// pairs with the one device it discovered.
type cliPairing struct {
	set      bool
	password string
	found    []v2.PairDiscoveredDevice
	paired   string
}

func (p *cliPairing) PairPasswordState() device.PairStatus {
	return device.PairStatus{Set: p.set, UpdatedAt: time.Unix(1700000000, 0)}
}
func (p *cliPairing) SetPairPassword(password string) (device.PairStatus, error) {
	if err := device.ValidatePassword(password); err != nil {
		return device.PairStatus{}, err
	}
	p.set, p.password = true, password
	return p.PairPasswordState(), nil
}
func (p *cliPairing) ClearPairPassword() error { p.set = false; return nil }
func (p *cliPairing) Discovered(context.Context) ([]v2.PairDiscoveredDevice, error) {
	return p.found, nil
}
func (p *cliPairing) PairWith(_ context.Context, _ device.PathKind, address, password, expected string) (device.Trust, error) {
	p.paired = address + " " + password
	return device.Trust{Document: device.Document{Name: "peer-b", DeviceID: "dev-" + cliPeerFingerprint}, Tags: []string{"tag:peer", "tag:device"}}, nil
}

const cliPeerFingerprint = "3f2a9c4e1b7d8a60f5e2c9b1d4a7e8f03c6b5a29d1e4f7a8b0c3d6e9f2a5b8c1"

// conformance: CTR-CTL-08 the CLI takes connection passwords from stdin or a file, never argv.
func Test_CLIControlPairPassword(t *testing.T) {
	admin := &cliPairing{found: []v2.PairDiscoveredDevice{{Name: "peer-b", Fingerprint: cliPeerFingerprint, Address: "192.168.31.15:7433", Path: "lan", DeviceID: "dev-" + cliPeerFingerprint, SeenAt: v2.Now()}}}
	state, _ := startKernelStubOptions(t, cliConfig, func(o *control.Options) { o.Pairing = admin })

	if got := runObject(t, state, "pair", "password", "show"); got["set"] != false {
		t.Fatalf("a new node has no password: %v", got)
	}
	controlStdin = strings.NewReader("correct horse battery staple\n")
	t.Cleanup(func() { controlStdin = os.Stdin })
	if got := runObject(t, state, "pair", "password", "set", "--password-stdin"); got["set"] != true || admin.password != "correct horse battery staple" {
		t.Fatalf("set from stdin: %v %q", got, admin.password)
	}
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("another long password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runObject(t, state, "pair", "password", "set", "--password-file", pwFile)
	if admin.password != "another long password" {
		t.Fatalf("set from file: %q", admin.password)
	}
	if out, err := runControl(t, state, "pair", "password", "set", "correct horse battery staple"); err == nil || strings.Contains(out, "horse") {
		t.Fatalf("a password on the command line was accepted: %v", err)
	}
	if _, err := runControl(t, state, "pair", "password", "set"); err == nil || !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("set without a source: %v", err)
	}
	controlStdin = strings.NewReader("short\n")
	if _, err := runControl(t, state, "pair", "password", "set", "--password-stdin"); err == nil {
		t.Fatal("a short password was accepted")
	}

	if got := runObject(t, state, "pair", "discover"); len(got["devices"].([]any)) != 1 {
		t.Fatalf("discover: %v", got)
	}
	controlStdin = strings.NewReader("correct horse battery staple\n")
	runObject(t, state, "pair", "connect", "--address", "192.168.31.15:7433", "--password-stdin")
	if admin.paired != "https://192.168.31.15:7433 correct horse battery staple" {
		t.Fatalf("connect: %q", admin.paired)
	}
	if _, err := runControl(t, state, "pair", "connect", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "--address") {
		t.Fatalf("connect without a target: %v", err)
	}

	if got := runObject(t, state, "pair", "password", "clear"); got["cleared"] != true || admin.set {
		t.Fatalf("clear: %v", got)
	}
}

func Test_PairExportReplacesWithPrivateFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pair.json")
	if err := os.WriteFile(file, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writePairExport(file, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "new" {
		t.Fatalf("%q %v", got, err)
	}
}

func Test_ControlIngressClientsRejectsPositionalArguments(t *testing.T) {
	_, err := output(t, func() error { return controlCommand([]string{"ingress", "clients", "unexpected"}) })
	if err == nil {
		t.Fatal("extra positional argument accepted")
	}
}
