package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/computer"
)

type desktopFixture struct{ inputs atomic.Int32 }

func (*desktopFixture) Info(context.Context) (computer.Info, error) {
	return computer.Info{ProtocolVersion: 1, Available: true, State: "ready", DesktopID: "desktop", Epoch: "epoch", LayoutVersion: "layout", Capabilities: []string{"screenshot", "pointer", "keyboard", "text"}}, nil
}
func (*desktopFixture) Capture(context.Context, computer.CaptureRequest) (computer.Frame, error) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 100, 80)))
	return computer.Frame{Epoch: "epoch", LayoutVersion: "layout", DisplayID: "main", Width: 100, Height: 80, Region: computer.Region{Width: 100, Height: 80}, Image: b.Bytes()}, nil
}
func (p *desktopFixture) Execute(_ context.Context, r computer.Execution) (computer.ActionResult, error) {
	p.inputs.Add(1)
	return computer.ActionResult{State: "executed", Executed: len(r.Actions)}, nil
}
func (*desktopFixture) Release(context.Context, computer.Execution) error { return nil }
func (*desktopFixture) Close() error                                      { return nil }

func TestComputerMCPImagesOwnershipAndHotDisable(t *testing.T) {
	path, store := configuration(t)
	b, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["agent"].(map[string]any)["computer"] = map[string]any{"enabled": true}
	b, _ = json.Marshal(doc)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	p := &desktopFixture{}
	a, e := New(store, WithComputerProvider(p))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	ts := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(ts.Close)
	one, two := connect(t, ts.URL+"/mcp"), connect(t, ts.URL+"/mcp")
	opened := object(t, tool(t, one, "computer.open", map[string]any{"mode": "control", "caller_id": "forged/identity"}))
	if opened["owner"] == "forged/identity" || opened["session_id"] == nil {
		t.Fatal(opened)
	}
	args := map[string]any{"session_id": opened["session_id"], "desktop_epoch": opened["desktop_epoch"], "lease_token": opened["lease_token"]}
	observed := tool(t, one, "computer.observe", args)
	if observed.IsError || len(observed.Content) != 2 {
		t.Fatal(observed)
	}
	img, ok := observed.Content[1].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || len(img.Data) == 0 {
		t.Fatal(observed.Content)
	}
	if out := object(t, tool(t, two, "computer.observe", args)); out["error"] != "not_owner" {
		t.Fatal(out)
	}
	args["action_id"] = "one"
	args["frame_id"] = object(t, observed)["frame_id"]
	args["actions"] = []map[string]any{{"type": "click", "x": 50, "y": 40}}
	if out := object(t, tool(t, one, "computer.act", args)); out["state"] != "executed" {
		t.Fatal(out)
	}
	if out := object(t, tool(t, one, "computer.act", args)); out["state"] != "executed" {
		t.Fatal(out)
	}
	if p.inputs.Load() != 1 {
		t.Fatal("MCP repeated the input")
	}
	args["actions"] = []map[string]any{{"type": "click", "x": 50, "y": 40, "unexpected": "x"}}
	if out := object(t, tool(t, one, "computer.act", args)); out["error"] != "invalid_args" {
		t.Fatal(out)
	}
	doc["agent"].(map[string]any)["computer"] = map[string]any{"enabled": false}
	b, _ = json.Marshal(doc)
	_ = os.WriteFile(path, b, 0600)
	if out := object(t, tool(t, one, "computer.info", map[string]any{})); out["error"] != "policy_denied" {
		t.Fatal(out)
	}
	if len(a.Computer.Sessions("")) != 0 {
		t.Fatal("hot disable kept the lease")
	}
}

func TestComputerObserverTagCannotReserveControl(t *testing.T) {
	path, store := configuration(t)
	b, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	cfg := doc["agent"].(map[string]any)
	cfg["computer"] = map[string]bool{"enabled": true}
	cfg["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"computer.info", "computer.open", "computer.observe"}, "backends": []string{}}}
	b, _ = json.Marshal(doc)
	_ = os.WriteFile(path, b, 0600)
	a, e := New(store, WithComputerProvider(&desktopFixture{}))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	ts := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(ts.Close)
	c := connect(t, ts.URL+"/mcp")
	if out := object(t, tool(t, c, "computer.open", map[string]any{"mode": "control"})); out["error"] != "unauthorized" {
		t.Fatal(out)
	}
	if out := tool(t, c, "computer.open", map[string]any{"mode": "observe"}); out.IsError {
		t.Fatal(out)
	}
}
