package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// listedComputerTools returns the tool names tools/list shows for a surface, on a gateway whose agent has the
// desktop tools enabled. The agent lists them even without a desktop executor, so the list is exactly the
// surface's selection.
func listedComputerTools(t *testing.T, s Surface) map[string]bool {
	t.Helper()
	g, _ := fixtureWith(t, false, func(c *config.Config) { c.Agent.Computer.Enabled = true })
	g.SetSurface(s)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	t.Cleanup(func() { _ = client.Close() })
	out, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range out.Tools {
		names[tool.Name] = true
	}
	return names
}

// conformance: CTR-GW-08
// Desktop tools stay out of the unified list until an admin lists the computer group or pins a tool. They are
// still reachable through tools.call, where authorization is checked on each call.
func Test_CTRGW08_ComputerStaysOffUnifiedListUntilAnAdminOptsIn(t *testing.T) {
	def := listedComputerTools(t, Surface{Mode: SurfaceUnified})
	if !def["fs.read"] {
		t.Fatal("the default unified list lost fs.read")
	}
	for name := range def {
		if strings.HasPrefix(name, "computer.") {
			t.Fatalf("default unified list shows %s", name)
		}
	}

	group := listedComputerTools(t, Surface{Mode: SurfaceUnified, ListedGroups: []string{"fs", "exec", "pty", "sys", "computer"}})
	if !group["computer.info"] || !group["computer.act"] {
		t.Fatalf("listing the computer group did not show its tools: %v", group)
	}

	pinned := listedComputerTools(t, Surface{Mode: SurfaceUnified, PinnedTools: []string{"computer.observe"}})
	if !pinned["computer.observe"] || pinned["computer.act"] {
		t.Fatalf("pinning computer.observe showed %v", pinned)
	}

	full := listedComputerTools(t, Surface{Mode: SurfaceFull})
	found := false
	for name := range full {
		if strings.HasSuffix(name, "computer.act") {
			found = true
		}
	}
	if !found {
		t.Fatal("the full surface lost the device's desktop tools, so the default check proves nothing")
	}
}
