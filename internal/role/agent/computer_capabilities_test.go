package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/tzf1003/xmesh/internal/computer"
)

// capsDesktop is a desktop whose reported capabilities a test can change between two reports.
type capsDesktop struct {
	desktopFixture
	mu   sync.Mutex
	caps []string
}

func (p *capsDesktop) Info(context.Context) (computer.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return computer.Info{ProtocolVersion: 1, Available: true, State: "ready", DesktopID: "desktop", Epoch: "epoch", LayoutVersion: "layout", Capabilities: slices.Clone(p.caps)}, nil
}

func (p *capsDesktop) report(caps ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = caps
}

// Element tools are listed only while the engine reports refs. The view follows each later report, so a
// switch of engine or a permission change needs no restart.
func TestComputerRefToolsFollowTheEngineCapabilities(t *testing.T) {
	file, store := configuration(t)
	raw, _ := os.ReadFile(file)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["agent"].(map[string]any)["computer"] = map[string]bool{"enabled": true}
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(file, out, 0600); err != nil {
		t.Fatal(err)
	}
	p := &capsDesktop{caps: []string{"screenshot", "pointer", "keyboard"}}
	a, err := New(store, WithComputerProvider(p))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")

	if _, err = a.Computer.Info(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return !slices.Contains(listedToolNames(t, client), "computer.snapshot") })
	names := listedToolNames(t, client)
	for _, want := range []string{"computer.info", "computer.observe", "computer.act"} {
		if !slices.Contains(names, want) {
			t.Fatal(want, "is hidden though it needs no capability")
		}
	}
	if slices.Contains(names, "computer.ref_act") || slices.Contains(names, "computer.ref_read") {
		t.Fatal("computer.ref_act is listed without refs")
	}

	p.report("screenshot", "pointer", "keyboard", "refs")
	if _, err = a.Computer.Info(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		names := listedToolNames(t, client)
		return slices.Contains(names, "computer.snapshot") && slices.Contains(names, "computer.ref_read") && slices.Contains(names, "computer.ref_act")
	})
}

// computer.ref_read advertises exactly the ops the catalog maps, and its schema admits only the properties of
// the op it names: a value is never offered, and text is not a property of is.
func Test_RefReadAdvertisesTheCatalogOps(t *testing.T) {
	spec := builtinSpecs()["computer.ref_read"]
	enum := spec.schema["properties"].(map[string]any)["op"].(map[string]any)["enum"].([]string)
	got := slices.Clone(enum)
	want := computer.MappedOps("computer.ref_read")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("computer.ref_read advertises %v, the catalog maps %v", got, want)
	}
	for _, op := range want {
		if len(computer.RefReadProperties(op)) == 0 {
			t.Fatalf("op %s has no readable properties", op)
		}
	}
	const base = `"session_id":"s","desktop_epoch":"e","snapshot_id":"s1","ref":"@s1:e1"`
	for _, bad := range []string{
		`{` + base + `,"op":"get","property":"value"}`,
		`{` + base + `,"op":"is","property":"text"}`,
		`{` + base + `,"op":"get","property":"text","text":"x"}`,
		`{` + base + `,"op":"get","property":"text","lease_token":"t"}`,
	} {
		if spec.validate([]byte(bad)) == nil {
			t.Fatalf("computer.ref_read accepted %s", bad)
		}
	}
	if err := spec.validate([]byte(`{` + base + `,"op":"is","property":"visible"}`)); err != nil {
		t.Fatalf("a valid is read was refused: %v", err)
	}
}

// The catalog and the built-in schemas describe the same surface: every mapped tool is a built-in, and the
// ops computer.ref_act advertises are exactly the ops the catalog maps.
func Test_ComputerCatalogAgreesWithTheBuiltins(t *testing.T) {
	specs := builtinSpecs()
	for _, tool := range computer.MappedTools() {
		if _, ok := specs[tool]; !ok {
			t.Fatalf("the catalog maps %s, which is not a built-in tool", tool)
		}
	}
	enum := specs["computer.ref_act"].schema["properties"].(map[string]any)["op"].(map[string]any)["enum"].([]string)
	got := slices.Clone(enum)
	want := computer.MappedOps("computer.ref_act")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("computer.ref_act advertises %v, the catalog maps %v", got, want)
	}
}
