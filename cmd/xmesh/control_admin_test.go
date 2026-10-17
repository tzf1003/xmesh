package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/mesh"
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

func Test_CLIControlPair(t *testing.T) {
	state, _ := startKernelStub(t)
	other, err := mesh.LoadCertificate(t.TempDir(), "peer-b")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// stdout carries the whole export reply; --out writes the document the peer imports
	exported := runObject(t, state, "pair", "export")["pair"].(map[string]any)
	if exported["name"] != "cli-node" || len(exported["fingerprint"].(string)) != 64 {
		t.Fatal(exported)
	}
	file := filepath.Join(dir, "cli-node.pair.json")
	written := runObject(t, state, "pair", "export", "--out", file)
	if written["file"] != file || written["fingerprint"] != exported["fingerprint"] {
		t.Fatal(written)
	}
	var onDisk v1.PairDocument
	raw, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(raw, &onDisk) != nil || onDisk.CertificatePEM == "" || onDisk.Fingerprint != exported["fingerprint"] {
		t.Fatalf("%v %s", err, raw)
	}

	peerFile := filepath.Join(dir, "peer-b.json")
	doc, _ := json.Marshal(mesh.Trusted{Name: "peer-b", Fingerprint: other.Fingerprint, CertificatePEM: string(other.PEM)})
	if err = os.WriteFile(peerFile, doc, 0600); err != nil {
		t.Fatal(err)
	}
	// a fingerprint that is not the certificate's is refused and nothing is stored
	if _, err = runControl(t, state, "pair", "import", "--file", peerFile, "--fingerprint", "00"+other.Fingerprint[2:]); err == nil || !strings.Contains(err.Error(), "fingerprint_mismatch") && !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	if _, err = os.Stat(filepath.Join(state, "lan", "peers.json")); err == nil {
		t.Fatal("a refused import left a trust record")
	}
	if _, err = runControl(t, state, "pair", "import", "--file", peerFile); err == nil || !strings.Contains(err.Error(), "--fingerprint") {
		t.Fatalf("import without a fingerprint: %v", err)
	}
	colon := strings.ToUpper(other.Fingerprint[:2] + ":" + other.Fingerprint[2:])
	imported := runObject(t, state, "pair", "import", "--file", peerFile, "--fingerprint", colon, "--tags", "tag:peer")
	if imported["name"] != "peer-b" || imported["fingerprint"] != other.Fingerprint || imported["restart_required"] != true || len(imported["tags"].([]any)) != 1 {
		t.Fatal(imported)
	}
	// the export reply itself is accepted as the pairing file
	third, err := mesh.LoadCertificate(t.TempDir(), "peer-c")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := json.Marshal(v1.PairExportResponse{Pair: v1.PairDocument{Name: "peer-c", Fingerprint: third.Fingerprint, CertificatePEM: string(third.PEM)}})
	if err = os.WriteFile(peerFile, wrapped, 0600); err != nil {
		t.Fatal(err)
	}
	if got := runObject(t, state, "pair", "import", "--file", peerFile, "--fingerprint", third.Fingerprint); got["name"] != "peer-c" {
		t.Fatal(got)
	}

	for _, bad := range [][]string{{"pair"}, {"pair", "swap"}, {"pair", "export", "--file", peerFile}, {"pair", "import", "--out", file}} {
		if _, err = runControl(t, state, bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
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

	if gone := runObject(t, state, "pats", "delete", "ci"); gone["deleted"] != true || gone["name"] != "ci" {
		t.Fatal(gone)
	}
	if _, err = os.Stat(file); err == nil {
		t.Fatal("token file survived delete")
	}
	if list := runObject(t, state, "pats", "list"); list["enforced"] != false {
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
	clients  []v1.IngressClient
	password string
}

func (i *cliIngress) Issuer() string                { return "https://x.example.com" }
func (i *cliIngress) Clients() []v1.IngressClient   { return i.clients }
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
	admin := &cliIngress{clients: []v1.IngressClient{{ClientID: "c1", ClientName: "Claude", RedirectURIs: []string{"https://claude.ai/cb"}, Scopes: []string{"devices:read"}, TokenEndpointAuthMethod: "none"}}}
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
