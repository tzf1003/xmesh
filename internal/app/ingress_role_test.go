package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
)

// ingressConfigFor is a configuration with the ingress role on a reverse tunnel; edit changes the
// ingress section.
func ingressConfigFor(t *testing.T, roles []string, edit func(section map[string]any)) *config.Config {
	t.Helper()
	section := map[string]any{"listen": "tunnel", "max_pty_per_client": 4, "oauth": map[string]any{"issuer": "https://mcp.example.com", "clients": map[string]any{"codex": map[string]any{"scopes": []string{"devices:read"}}}}}
	if edit != nil {
		edit(section)
	}
	doc := map[string]any{"node": map[string]any{"name": "ingress-node", "roles": roles}, "mesh": map[string]any{"mode": "tailnet", "control_url": "https://hs.example.com", "authkey_file": "/etc/xmesh/authkey"}, "gateway": map[string]any{"listen": address(t)}, "ingress": section, "agent": map[string]any{"cwd_policy": map[string]any{"allow": []string{}}}}
	b, _ := json.Marshal(doc)
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// ingressRuntime is a role runtime that can run the ingress on a reverse tunnel at the returned address.
func ingressRuntime(t *testing.T, cfg *config.Config) (rt *roleRuntime, tunnel string, ctx context.Context) {
	t.Helper()
	state := t.TempDir()
	password := filepath.Join(state, "oauth-password")
	if err := os.WriteFile(password, []byte("a long enough password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tunnel = address(t)
	rt = newRoleRuntime(gatewayDepsFor(t, cfg), state, func(e error) { t.Error(e) })
	rt.configureIngress(ingressDeps{self: identity.Principal{Node: "n", Tags: []string{"tag:ingress"}}, stateDir: state, passwordFile: password, tunnelListen: tunnel}, false)
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		rt.close()
	})
	return rt, tunnel, ctx
}

// The ingress starts and stops with the role, the control channel sees the one that runs now, and
// the node's other roles are not involved.
func Test_RoleRuntimeStartsAndStopsTheIngress(t *testing.T) {
	cfg := ingressConfigFor(t, []string{"ingress"}, nil)
	rt, tunnel, ctx := ingressRuntime(t, cfg)
	if rt.ingressAdmin() != nil {
		t.Fatal("an ingress is reported before it started")
	}
	if err := rt.startIngress(cfg); err != nil {
		t.Fatal(err)
	}
	rt.begin(ctx)
	if !answers(tunnel) || rt.ingressAdmin() == nil || rt.ingressAdmin().Issuer() != "https://mcp.example.com" {
		t.Fatal("the ingress does not answer")
	}

	rt.submit(ingressConfigFor(t, []string{"gateway"}, nil))
	if problems := rt.Settle(ctx, false); len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	if rt.ingressAdmin() != nil {
		t.Fatal("the control channel still sees a stopped ingress")
	}
	eventually(t, "the tunnel stops accepting", func() bool { return refusesConnections(tunnel) })

	rt.submit(cfg)
	if problems := rt.Settle(ctx, false); len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	eventually(t, "the ingress answers again", func() bool { return answers(tunnel) })
	if rt.ingressAdmin() == nil {
		t.Fatal("the restarted ingress is not reported")
	}
}

// A change of the ingress section restarts the ingress with the new section.
func Test_RoleRuntimeRestartsTheIngressForANewSection(t *testing.T) {
	cfg := ingressConfigFor(t, []string{"ingress"}, nil)
	rt, tunnel, ctx := ingressRuntime(t, cfg)
	if err := rt.startIngress(cfg); err != nil {
		t.Fatal(err)
	}
	rt.begin(ctx)
	before := rt.ingress

	next := ingressConfigFor(t, []string{"ingress"}, func(section map[string]any) { section["max_pty_per_client"] = 2 })
	rt.submit(next)
	if problems := rt.Settle(ctx, false); len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	if section, ok := rt.ingressConfig(); !ok || section.MaxPTYPerClient != 2 || rt.ingress == before {
		t.Fatalf("the ingress did not move to the new section: %+v", section)
	}
	eventually(t, "the restarted ingress answers", func() bool { return answers(tunnel) })

	// A generation that leaves the section alone leaves the ingress alone.
	same := rt.ingress
	rt.submit(next)
	rt.Settle(ctx, false)
	if rt.ingress != same {
		t.Fatal("an unchanged section restarted the ingress")
	}
}

// A section that cannot be used leaves the running ingress as it was, and says why.
func Test_RoleRuntimeKeepsTheIngressWhenTheNewSectionIsRefused(t *testing.T) {
	cfg := ingressConfigFor(t, []string{"ingress"}, nil)
	rt, tunnel, ctx := ingressRuntime(t, cfg)
	if err := rt.startIngress(cfg); err != nil {
		t.Fatal(err)
	}
	rt.begin(ctx)
	before := rt.ingress

	// A certificate that cannot be read is refused before anything is stopped.
	rt.submit(ingressConfigFor(t, []string{"ingress"}, func(section map[string]any) {
		section["listen"], section["domain"], section["tls"] = "public:443", "mcp.example.com", "file"
		section["cert_file"], section["key_file"] = filepath.Join(t.TempDir(), "missing.crt"), filepath.Join(t.TempDir(), "missing.key")
	}))
	problems := rt.Settle(ctx, false)
	if len(problems) != 1 || problems[0].Section != "ingress" || problems[0].Message == "" {
		t.Fatalf("problems = %+v", problems)
	}
	if rt.ingress != before || !answers(tunnel) {
		t.Fatal("a refused section took the ingress down")
	}
	if rt.failureCount() != 1 {
		t.Fatalf("failures = %d", rt.failureCount())
	}

	// Going back to the section that runs clears the report.
	rt.submit(cfg)
	if problems = rt.Settle(ctx, false); len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
}

// A section that fails only once the old ingress has made room for it puts the old one back. Unrelated
// changes do not try it again, which would tear the ingress down each time; an explicit retry does.
func Test_RoleRuntimeRestoresTheIngressWhenTheNewOneFailsToStart(t *testing.T) {
	cfg := ingressConfigFor(t, []string{"ingress"}, nil)
	rt, tunnel, ctx := ingressRuntime(t, cfg)
	if err := rt.startIngress(cfg); err != nil {
		t.Fatal(err)
	}
	rt.begin(ctx)

	// A PTY budget journal below a regular file cannot be opened.
	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	broken := ingressConfigFor(t, []string{"ingress"}, func(section map[string]any) { section["pty_budget_file"] = filepath.Join(file, "journal.json") })
	rt.submit(broken)
	problems := rt.Settle(ctx, false)
	if len(problems) != 1 || problems[0].Section != "ingress" {
		t.Fatalf("problems = %+v", problems)
	}
	eventually(t, "the old section answers again", func() bool { return answers(tunnel) })
	if section, ok := rt.ingressConfig(); !ok || section.PTYBudgetFile != "" {
		t.Fatalf("the old section was not restored: %+v", section)
	}
	restored := rt.ingress

	other := ingressConfigFor(t, []string{"ingress"}, func(section map[string]any) { section["pty_budget_file"] = filepath.Join(file, "journal.json") })
	other.Gateway.Quota.CallsPerHour = 7
	rt.submit(other)
	rt.Settle(ctx, false)
	if rt.ingress != restored {
		t.Fatal("an unrelated change tore the ingress down again")
	}
	if rt.Settle(ctx, true); rt.ingress == restored {
		t.Fatal("an explicit retry did not try the section again")
	}
	if !answers(tunnel) {
		t.Fatal("the ingress is down after the retry")
	}
}
