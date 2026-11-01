package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// devices.list keeps `backends` as names and adds `backend_info` with the description, policy and
// the number of the backend's tools the caller may use.
func Test_CTRGW02_DevicesListBackendInfo(t *testing.T) {
	device := newScripted()
	device.tool("sys.info", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return agent.Result(map[string]any{"os": "linux", "limits": map[string]any{}, "backends": []map[string]any{
			{"name": "ida", "policy": "exclusive", "favorite": true, "desc": "反汇编"},
			{"name": "abc", "policy": "shared", "favorite": false},
		}}, nil), nil
	})
	ok := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return agent.Result(map[string]any{"ok": true}, nil), nil
	}
	schema := map[string]any{"type": "object"}
	device.tool("ida.open", schema, ok)
	device.tool("ida.decompile", schema, ok)
	device.tool("abc.run", schema, ok)
	g, client := device.gateway(t, Options{})
	waitFor(t, "device", 5*time.Second, func() bool { found, _ := g.snapshot(); return len(found) == 1 })
	g.pollInfo(time.Now())
	var row map[string]any
	waitFor(t, "backend_info", 10*time.Second, func() bool {
		rows := object(t, call(t, client, "devices.list", map[string]any{}))["devices"].([]any)
		row = rows[0].(map[string]any)
		info, _ := row["backend_info"].([]any)
		return len(info) == 2
	})
	if names, _ := row["backends"].([]any); len(names) != 2 || names[0] != "abc" || names[1] != "ida" {
		t.Fatal("backends changed", row["backends"])
	}
	info := row["backend_info"].([]any)
	abc, ida := info[0].(map[string]any), info[1].(map[string]any)
	if abc["name"] != "abc" || abc["policy"] != "shared" || abc["favorite"] != false || abc["tools"] != float64(1) {
		t.Fatal(abc)
	}
	if _, has := abc["desc"]; has {
		t.Fatal("empty desc must be omitted", abc)
	}
	if ida["name"] != "ida" || ida["desc"] != "反汇编" || ida["policy"] != "exclusive" || ida["favorite"] != true || ida["tools"] != float64(2) {
		t.Fatal(ida)
	}
}
