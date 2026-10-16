package meshadmin

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tailscale.com/tsnet"
)

// These tests need a real Headscale binary: XMESH_TEST_HEADSCALE_BIN=/path/to/headscale
// (the Headscale CI workflow sets it). They run Headscale in policy.mode=database
// with a throwaway SQLite database and one DERP region from a derp.paths file.

type testHeadscale struct {
	bin, dir, cfg, url, apiKey string
	cmd                        *exec.Cmd
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func (h *testHeadscale) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(h.bin, append([]string{"-c", h.cfg}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("headscale %v: %v\n%s%s", args, err, out.String(), errb.String())
	}
	return strings.TrimSpace(out.String())
}

func startHeadscale(t *testing.T, derpFile []byte, policyMode string) *testHeadscale {
	t.Helper()
	bin := os.Getenv("XMESH_TEST_HEADSCALE_BIN")
	if bin == "" {
		t.Skip("set XMESH_TEST_HEADSCALE_BIN to run the Headscale integration tests")
	}
	dir := t.TempDir()
	port := freePort(t)
	if derpFile == nil {
		derpFile = []byte("regions:\n  900:\n    regionid: 900\n    regioncode: seed\n    regionname: Seed\n    nodes:\n      - name: 900a\n        regionid: 900\n        hostname: seed.example.test\n        ipv4: 198.51.100.7\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "derp.yaml"), derpFile, 0o600); err != nil {
		t.Fatal(err)
	}
	policy := ""
	if policyMode == "file" {
		policy = filepath.Join(dir, "policy.json")
		_ = os.WriteFile(policy, []byte(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`), 0o600)
	}
	cfg := strings.Join([]string{
		fmt.Sprintf("server_url: http://127.0.0.1:%d", port),
		fmt.Sprintf("listen_addr: 127.0.0.1:%d", port),
		fmt.Sprintf("metrics_listen_addr: 127.0.0.1:%d", freePort(t)),
		fmt.Sprintf("grpc_listen_addr: 127.0.0.1:%d", freePort(t)),
		"grpc_allow_insecure: true",
		fmt.Sprintf("noise:\n  private_key_path: %s", filepath.Join(dir, "noise.key")),
		"prefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a:115c:a1e0::/48\n  allocation: sequential",
		fmt.Sprintf("database:\n  type: sqlite\n  sqlite:\n    path: %s", filepath.Join(dir, "db.sqlite")),
		fmt.Sprintf("derp:\n  server:\n    enabled: false\n  urls: []\n  paths:\n    - %s\n  auto_update_enabled: false", filepath.Join(dir, "derp.yaml")),
		"dns:\n  magic_dns: false\n  base_domain: mesh.test\n  override_local_dns: false\n  nameservers:\n    global: []",
		fmt.Sprintf("policy:\n  mode: %s\n  path: %q", policyMode, policy),
		fmt.Sprintf("unix_socket: %s", filepath.Join(dir, "hs.sock")),
		`unix_socket_permission: "0770"`,
		"log:\n  level: warn",
		"",
	}, "\n")
	h := &testHeadscale{bin: bin, dir: dir, cfg: filepath.Join(dir, "config.yaml"), url: fmt.Sprintf("http://127.0.0.1:%d", port)}
	if err := os.WriteFile(h.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var logbuf bytes.Buffer
	h.cmd = exec.Command(bin, "-c", h.cfg, "serve")
	h.cmd.Stdout, h.cmd.Stderr = &logbuf, &logbuf
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.cmd.Process.Kill()
		_, _ = h.cmd.Process.Wait()
		if t.Failed() {
			t.Logf("headscale log:\n%s", logbuf.String())
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("headscale did not start:\n%s", logbuf.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.run(t, "users", "create", "xmesh")
	h.apiKey = h.run(t, "apikeys", "create", "-e", "1h")
	return h
}

func (h *testHeadscale) provider(t *testing.T) Provider {
	t.Helper()
	p, err := NewProvider(Credential{Kind: KindHeadscale, Type: CredAPIKey, URL: h.url, Secret: h.apiKey}, AccessWrite)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// joinNode registers a tsnet node (no TUN) with a pre-auth key and returns its name.
func (h *testHeadscale) joinNode(t *testing.T, p Provider, name string, tags []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	key, err := p.CreateAuthKey(ctx, AuthKeyRequest{Tags: tags, TTL: time.Hour, Ephemeral: true, User: "xmesh"})
	if err != nil {
		t.Fatalf("auth key: %v", err)
	}
	s := &tsnet.Server{Dir: filepath.Join(h.dir, "node-"+name), Hostname: name, ControlURL: h.url, AuthKey: key.Secret, Ephemeral: true, Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Up(ctx); err != nil {
		t.Fatalf("node %s did not join: %v", name, err)
	}
	return name
}

// conformance: CTR-MESH-01 RUL-20 RUL-21
func TestHeadscaleScenario(t *testing.T) {
	h := startHeadscale(t, nil, "database")
	p := h.provider(t)
	h.joinNode(t, p, "ix-a", []string{TagDevice})
	devs, err := p.Devices(context.Background())
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices: %v %+v", err, devs)
	}
	if devs[0].NodeKey == "" || devs[0].User == "" {
		t.Fatalf("device fields missing: %+v", devs[0])
	}
	// Seed the tagOwners the template installs, so SetTags is allowed.
	providerScenario(t, p, "xmesh@", devs[0].ID)
}

// conformance: RUL-20 RUL-21
func TestHeadscaleFileModeIsReadOnly(t *testing.T) {
	h := startHeadscale(t, nil, "file")
	p := h.provider(t)
	ctx := context.Background()
	caps, err := p.Capabilities(ctx)
	if err != nil || caps.PolicyWrite {
		t.Fatalf("file mode must report PolicyWrite=false: %v %+v", err, caps)
	}
	plan, err := PlanPolicy(ctx, p, "x", func(c string) (string, []string, error) { return c + "\n// x\n", nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if plan.Writable {
		t.Fatal("plan claims writable")
	}
	if _, err := plan.Apply(ctx, p, nil, true); !errorsIs(err, ErrUnsupported) {
		t.Fatalf("apply in file mode: %v", err)
	}
}

func TestHeadscaleValidateRejectsBadPolicy(t *testing.T) {
	h := startHeadscale(t, nil, "database")
	p := h.provider(t)
	err := p.ValidatePolicy(context.Background(), `{"acls":[{"action":"accept","src":["nope"],"dst":["*:*"]}]}`)
	var ve *ValidationError
	if !errorsAs(err, &ve) || !strings.Contains(ve.Message, "nope") {
		t.Fatalf("want ValidationError mentioning the bad alias, got %v", err)
	}
}

// The DERP file we generate must be accepted by Headscale's own loader: a
// malformed or mis-keyed file makes it refuse to start.
// conformance: CTR-MESH-04
func TestHeadscaleLoadsGeneratedDERPFile(t *testing.T) {
	specs, err := AssignRegionIDs([]DERPRegionSpec{
		{Code: "lab", Name: "Lab", Hostname: "derp.example.test", IPv4: "198.51.100.9", IPv6: "2001:db8::9"},
		{Code: "alt", Name: "Alt", Hostname: "derp2.example.test", IPv4: "198.51.100.10", DERPPort: 8443, STUNPort: 3479},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, warns, err := HeadscaleDERPConfig(specs, DERPOptions{OmitDefaultRegions: true}, "/etc/headscale/xmesh-derp.yaml")
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", err, warns)
	}
	if !strings.Contains(string(out.File), "hostname: derp.example.test") || !strings.Contains(out.Snippet, "urls: []") {
		t.Fatalf("unexpected artifacts:\n%s\n%s", out.File, out.Snippet)
	}
	h := startHeadscale(t, out.File, "database")
	// Headscale is up with only our regions; the policy API still answers.
	if _, err := h.provider(t).Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
}
