package agent

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

// The contract's enum for agent.disabled_tools must name exactly the built-in tools, so a switch in the UI
// can never write a name the kernel does not know.
func Test_DisabledToolsEnumMatchesBuiltins(t *testing.T) {
	raw, err := schemas.Files.ReadFile("config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties struct {
			Agent struct {
				Properties struct {
					Disabled struct {
						Items struct {
							Enum []string `json:"enum"`
						} `json:"items"`
					} `json:"disabled_tools"`
				} `json:"properties"`
			} `json:"agent"`
		} `json:"properties"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var want []string
	for name := range builtinSpecs() {
		want = append(want, name)
	}
	got := slices.Clone(doc.Properties.Agent.Properties.Disabled.Items.Enum)
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("schema enum %v, built-in tools %v", got, want)
	}
}

// Switching a built-in tool off in the file removes it from tools/list and refuses calls to it; switching
// it back on restores it. Both happen on the next reload, with no restart.
func Test_DisabledBuiltinToolsLeaveTheList(t *testing.T) {
	path, store := configuration(t)
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if !slices.Contains(listedToolNames(t, client), "fs.stat") {
		t.Fatal("fs.stat is not listed before it is switched off")
	}

	rewriteConfigFile(t, path, func(agent map[string]any) { agent["disabled_tools"] = []string{"fs.stat"} })
	waitUntil(t, func() bool { return !slices.Contains(listedToolNames(t, client), "fs.stat") })
	if _, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "fs.stat", Arguments: map[string]any{"path": path}}); err == nil {
		t.Fatal("call to a disabled tool was not refused")
	}

	rewriteConfigFile(t, path, func(agent map[string]any) { delete(agent, "disabled_tools") })
	waitUntil(t, func() bool { return slices.Contains(listedToolNames(t, client), "fs.stat") })
	if o := tool(t, client, "fs.stat", map[string]any{"path": path}); o.IsError {
		t.Fatal("re-enabled tool still refused", object(t, o))
	}
}

func listedToolNames(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	list, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, listed := range list.Tools {
		names = append(names, listed.Name)
	}
	return names
}

// rewriteConfigFile edits the agent section of the JSON configuration file the store reloads.
func rewriteConfigFile(t *testing.T, path string, edit func(agent map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc["agent"].(map[string]any))
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached after reload")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
