package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
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

func TestPolicyReloadFencesCachedActionImagesAndRenewalsOverMCP(t *testing.T) {
	for _, operation := range []string{"action_status", "duplicate", "renew-control", "renew-observe"} {
		for _, revoke := range []string{"tag", "disabled_tool"} {
			t.Run(operation+"/"+revoke, func(t *testing.T) {
				file, store := configuration(t)
				raw, _ := os.ReadFile(file)
				var doc map[string]any
				_ = json.Unmarshal(raw, &doc)
				cfg := doc["agent"].(map[string]any)
				cfg["computer"] = map[string]bool{"enabled": true}
				cfg["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"computer.*"}, "backends": []string{}}}
				write := func() {
					data, _ := json.Marshal(doc)
					if err := os.WriteFile(file, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				write()
				a, err := New(store, WithComputerProvider(&desktopFixture{}))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(a.Close)
				server := httptest.NewServer(a.HTTP(Local))
				t.Cleanup(server.Close)
				client := connect(t, server.URL+"/mcp")
				mode := "control"
				denied := "computer.observe"
				if operation == "renew-control" {
					denied = "computer.act"
				}
				if operation == "renew-observe" {
					mode = "observe"
				}
				session := object(t, tool(t, client, "computer.open", map[string]any{"mode": mode}))
				identity := map[string]any{"session_id": session["session_id"], "desktop_epoch": session["desktop_epoch"], "lease_token": session["lease_token"]}
				if session["lease_token"] == nil {
					delete(identity, "lease_token")
				}
				action := map[string]any{}
				for k, v := range identity {
					action[k] = v
				}
				action["action_id"] = "cached"
				action["actions"] = []map[string]any{{"type": "wait", "duration_ms": 1}}
				action["observe_after"] = true
				if operation == "action_status" || operation == "duplicate" {
					before := tool(t, client, "computer.act", action)
					if before.IsError || len(before.Content) != 2 {
						t.Fatal(before)
					}
				}
				if revoke == "tag" {
					grants := []string{"computer.info", "computer.open", "computer.observe", "computer.act", "computer.action_status", "computer.renew", "computer.close"}
					kept := []string{}
					for _, grant := range grants {
						if grant != denied {
							kept = append(kept, grant)
						}
					}
					cfg["authz"] = map[string]any{"tag:peer": map[string]any{"tools": kept, "backends": []string{}}}
				} else {
					cfg["disabled_tools"] = []string{denied}
				}
				write()
				name := "computer.renew"
				args := identity
				if operation == "action_status" {
					name = "computer.action_status"
					args["action_id"] = "cached"
				}
				if operation == "duplicate" {
					name = "computer.act"
					args = action
				}
				after := tool(t, client, name, args)
				if !after.IsError || object(t, after)["error"] != "policy_denied" {
					t.Fatal(after)
				}
				for _, content := range after.Content {
					if _, ok := content.(*mcp.ImageContent); ok {
						t.Fatal("revoked cached image escaped")
					}
				}
				if len(a.Computer.Sessions("")) != 0 {
					t.Fatal("revoked session retained capacity")
				}
			})
		}
	}
}

type revocableObservationFixture struct {
	desktopFixture
	block    atomic.Bool
	stage    string
	entered  chan struct{}
	resume   chan struct{}
	captures atomic.Int32
}

func (p *revocableObservationFixture) gate(ctx context.Context) error {
	if !p.block.Load() {
		return nil
	}
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-p.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *revocableObservationFixture) Info(ctx context.Context) (computer.Info, error) {
	if p.stage == "info" {
		if err := p.gate(ctx); err != nil {
			return computer.Info{}, err
		}
	}
	return p.desktopFixture.Info(ctx)
}
func (p *revocableObservationFixture) Capture(ctx context.Context, r computer.CaptureRequest) (computer.Frame, error) {
	p.captures.Add(1)
	if p.stage == "capture" {
		if err := p.gate(ctx); err != nil {
			return computer.Frame{}, err
		}
	}
	return p.desktopFixture.Capture(ctx, r)
}

func TestObservationPolicyReloadFencesInflightMCPImages(t *testing.T) {
	for _, stage := range []string{"info", "capture"} {
		for _, policy := range []string{"tag", "disabled_tool"} {
			t.Run(stage+"/"+policy, func(t *testing.T) {
				file, store := configuration(t)
				b, _ := os.ReadFile(file)
				var doc map[string]any
				_ = json.Unmarshal(b, &doc)
				cfg := doc["agent"].(map[string]any)
				cfg["computer"] = map[string]any{"enabled": true}
				cfg["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"computer.*"}, "backends": []string{}}}
				write := func() {
					b, _ := json.Marshal(doc)
					if err := os.WriteFile(file, b, 0600); err != nil {
						t.Fatal(err)
					}
				}
				write()
				p := &revocableObservationFixture{stage: stage, entered: make(chan struct{}, 1), resume: make(chan struct{})}
				a, err := New(store, WithComputerProvider(p))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(a.Close)
				ts := httptest.NewServer(a.HTTP(Local))
				t.Cleanup(ts.Close)
				client := connect(t, ts.URL+"/mcp")
				s := object(t, tool(t, client, "computer.open", map[string]any{"mode": "observe"}))
				if s["session_id"] == nil {
					t.Fatal(s)
				}
				p.block.Store(true)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				type outcome struct {
					result *mcp.CallToolResult
					err    error
				}
				result := make(chan outcome, 1)
				go func() {
					r, e := client.CallTool(ctx, &mcp.CallToolParams{Name: "computer.observe", Arguments: map[string]any{"session_id": s["session_id"], "desktop_epoch": s["desktop_epoch"]}})
					result <- outcome{r, e}
				}()
				select {
				case <-p.entered:
				case <-ctx.Done():
					t.Fatal("observation did not reach native preflight")
				}
				if policy == "tag" {
					cfg["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"computer.info", "computer.open"}, "backends": []string{}}}
				} else {
					cfg["disabled_tools"] = []string{"computer.observe"}
				}
				write()
				close(p.resume)
				select {
				case out := <-result:
					if out.err != nil {
						t.Fatal(out.err)
					}
					v := object(t, out.result)
					if v["error"] != "policy_denied" {
						t.Fatal(v)
					}
					for _, content := range out.result.Content {
						if _, ok := content.(*mcp.ImageContent); ok {
							t.Fatal("revoked screenshot was returned")
						}
					}
				case <-ctx.Done():
					t.Fatal("revoked observation did not finish")
				}
				if stage == "info" && p.captures.Load() != 0 {
					t.Fatal("revoked observation reached native capture")
				}
			})
		}
	}
}

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

func TestComputerSchemasExposeSupportedActionsAndCoordinateRequirements(t *testing.T) {
	specs := computerSpecs()
	for _, example := range []struct {
		tool, args string
		valid      bool
	}{
		{"computer.open", `{"mode":"control","ttl_seconds":30}`, true},
		{"computer.open", `{"mode":"control","interaction":"background","target_id":"window-one"}`, true},
		{"computer.open", `{"mode":"control","interaction":"background"}`, false},
		{"computer.open", `{"mode":"observe","interaction":"background"}`, false},
		{"computer.open", `{"mode":"control","interaction":"foreground"}`, true},
		{"computer.open", `{"mode":"control","interaction":"foreground","target_id":"window-one"}`, false},
		{"computer.open", `{"mode":"control","interaction":"global"}`, false},
		{"computer.open", `{"mode":"operator"}`, false},
		{"computer.open", `{"mode":"observe","ttl_seconds":601}`, false},
		{"computer.observe", `{"session_id":"s","desktop_epoch":"e","max_width":8193}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"click","x":20,"y":30}]}`, true},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"click","x":20,"y":30,"delta_y":10}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"move","x":20,"y":30,"clicks":2}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"scroll","x":20,"y":30}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"scroll","x":20,"y":30,"delta_y":0}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"scroll","x":20,"y":30,"delta_y":1}]}`, true},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"move","x":20,"y":30,"button":"left"}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"click","x":20,"y":30,"duration_ms":10}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"drag","x":20,"y":30,"to_x":1,"to_y":1,"button":"right","duration_ms":10}]}`, true},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"click"}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"drag","x":20,"y":30}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"key","keys":[]}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"shell"}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"wait"}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"wait","duration_ms":0}]}`, false},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"wait","duration_ms":1}]}`, true},
		{"computer.act", `{"session_id":"s","desktop_epoch":"e","lease_token":"1","action_id":"a","actions":[{"type":"drag","x":0,"y":0,"to_x":1,"to_y":1,"duration_ms":0}]}`, true},
	} {
		if err := specs[example.tool].validate(json.RawMessage(example.args)); (err == nil) != example.valid {
			t.Fatal("computer contract validation", example.tool, example.args, err)
		}
		compiler := jsonschema.NewCompiler()
		raw, _ := json.Marshal(specs[example.tool].schema)
		var published, arguments any
		_ = json.Unmarshal(raw, &published)
		_ = json.Unmarshal([]byte(example.args), &arguments)
		if err := compiler.AddResource("urn:published-computer-schema", published); err != nil {
			t.Fatal(err)
		}
		advertised, err := compiler.Compile("urn:published-computer-schema")
		if err != nil {
			t.Fatal(err)
		}
		if err := advertised.Validate(arguments); (err == nil) != example.valid {
			t.Fatal("advertised computer schema", example.tool, example.args, err)
		}
	}
	if description := specs["computer.act"].desc; !strings.Contains(description, "image pixel") || !strings.Contains(description, "unknown") {
		t.Fatal("Agent action guidance omits observation or uncertainty semantics", description)
	}
}
