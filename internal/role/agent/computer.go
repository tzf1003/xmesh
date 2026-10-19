package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func WithComputerProvider(p computer.Provider) Option {
	return func(a *Agent) { a.computerProvider = p }
}

func (a *Agent) callComputer(ctx context.Context, cfg *config.Config, caller identity.Caller, name string, raw json.RawMessage) *mcp.CallToolResult {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(raw, &args)
	for _, key := range ignoredArguments {
		delete(args, key)
	}
	raw, _ = json.Marshal(args)
	var out any
	var err error
	switch name {
	case "computer.info":
		out, err = a.Computer.Info(ctx)
	case "computer.open":
		var r computer.OpenRequest
		err = computer.Decode(raw, &r)
		if err == nil && r.Mode == "control" && !Allowed(cfg, caller, "computer.act", "") {
			return Result(nil, toolerr.New("unauthorized", "调用方未获桌面控制授权", "tool", name, "tag", strings.Join(caller.Tags, ",")))
		}
		if err == nil {
			out, err = a.Computer.Open(ctx, caller.ID, r)
		}
	case "computer.observe":
		var r computer.ObserveRequest
		err = computer.Decode(raw, &r)
		if err == nil {
			var f computer.Frame
			f, err = a.Computer.Observe(ctx, caller.ID, r)
			if err == nil {
				return computerResult(f, &f)
			}
		}
	case "computer.act":
		var r computer.ActRequest
		err = computer.Decode(raw, &r)
		if err == nil {
			var v computer.ActionResult
			v, err = a.Computer.Act(ctx, caller.ID, r)
			if err == nil {
				return computerResult(v, v.Frame)
			}
		}
	case "computer.action_status":
		var r computer.StatusRequest
		err = computer.Decode(raw, &r)
		if err == nil {
			var v computer.ActionResult
			v, err = a.Computer.ActionStatus(caller.ID, r)
			if err == nil {
				return computerResult(v, v.Frame)
			}
		}
	case "computer.renew":
		var r computer.RenewRequest
		err = computer.Decode(raw, &r)
		if err == nil {
			out, err = a.Computer.Renew(caller.ID, r)
		}
	case "computer.close":
		var r computer.SessionRequest
		err = computer.Decode(raw, &r)
		if err == nil {
			err = a.Computer.CloseSession(caller.ID, r)
			out = map[string]bool{"closed": err == nil}
		}
	case "computer.sessions":
		out = map[string]any{"sessions": a.Computer.Sessions(caller.ID)}
	}
	if err != nil {
		err = computer.Envelope(err)
	}
	return Result(out, err)
}

func computerResult(out any, f *computer.Frame) *mcp.CallToolResult {
	r := Result(out, nil)
	if f != nil && len(f.Image) > 0 {
		r.Content = append(r.Content, &mcp.ImageContent{MIMEType: f.MIMEType, Data: f.Image})
	}
	return r
}

func computerSpecs() map[string]spec {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	region := map[string]any{"type": "object", "description": "Crop relative to the selected display in the display's system coordinate units. Returned input coordinates are image pixels.", "additionalProperties": false, "required": []string{"x", "y", "width", "height"}, "properties": map[string]any{"x": map[string]any{"type": "integer", "minimum": 0}, "y": map[string]any{"type": "integer", "minimum": 0}, "width": map[string]any{"type": "integer", "minimum": 1}, "height": map[string]any{"type": "integer", "minimum": 1}}}
	actionProps := map[string]any{}
	for _, k := range strings.Fields("type button text") {
		actionProps[k] = str()
	}
	for _, k := range strings.Fields("x y to_x to_y clicks delta_x delta_y duration_ms") {
		actionProps[k] = integer()
	}
	actionProps["type"] = map[string]any{"type": "string", "enum": []string{"move", "click", "drag", "scroll", "key", "text", "wait"}}
	for _, k := range strings.Fields("x y to_x to_y") {
		actionProps[k] = map[string]any{"type": "integer", "minimum": 0, "description": "Pixel coordinate in the image returned by frame_id. The target converts cropping, scaling and display origin."}
	}
	actionProps["button"] = map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}, "default": "left"}
	actionProps["clicks"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 3, "description": "Click count; omitted or zero means one click."}
	actionProps["duration_ms"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 2000, "description": "Finite drag or wait duration. Explicit durations in a batch total at most 4000 ms."}
	for _, k := range []string{"delta_x", "delta_y"} {
		actionProps[k] = map[string]any{"type": "integer", "minimum": -10000, "maximum": 10000, "description": "Discrete wheel steps; positive means right or down. The pointer moves to x,y before scrolling."}
	}
	actionProps["text"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 65536, "description": "Unicode text, limited to 64 KiB UTF-8. Uses native text input; clipboard permission is separate."}
	actionProps["keys"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 32}, "description": "One finite chord, pressed in order and released in reverse. Supports single characters, Control, Alt, Shift, Meta/Command/Win, Enter, Tab, Escape, Backspace, Delete, Space, arrows, Home, End, PageUp, PageDown and F1-F12."}
	when := func(types []string, fields ...string) map[string]any {
		return map[string]any{"if": map[string]any{"properties": map[string]any{"type": map[string]any{"enum": types}}}, "then": map[string]any{"required": fields}}
	}
	action := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": actionProps,
		"allOf": []any{when([]string{"move", "click", "drag", "scroll"}, "x", "y"), when([]string{"drag"}, "to_x", "to_y"), when([]string{"key"}, "keys"), when([]string{"text"}, "text"), when([]string{"wait"}, "duration_ms")}}
	definitions := map[string][3]string{
		"computer.info":          {"Inspect desktop availability, system permissions, desktop_epoch, displays and supported screenshot/input capabilities. Missing system permission requires local setup.", "", ""},
		"computer.open":          {"Open an observation or exclusive control session. One controller owns the whole desktop, including all displays. Keep session_id, desktop_epoch and the control lease_token; renew before expiry and close when finished.", "mode", "desktop_id mode ttl_seconds"},
		"computer.observe":       {"Capture the current desktop as native MCP image content plus frame metadata. Select a display if needed. Pointer actions must use the returned image pixel coordinates and frame_id. Observe again after UI changes.", "session_id desktop_epoch", "session_id desktop_epoch lease_token display_id max_width region"},
		"computer.act":           {"Submit 1-8 finite mouse, keyboard, text or wait actions, with a five-second queue/execution budget. Pointer actions require a fresh frame_id and image pixel coordinates. Reuse action_id only for the identical request. After a lost response query action_status; unknown means observe before deciding what to do. executed means input was submitted: verify the business result in a new observation.", "session_id desktop_epoch lease_token action_id actions", "session_id desktop_epoch lease_token action_id frame_id actions observe_after"},
		"computer.action_status": {"Query an accepted action_id after a lost response. States: accepted, executed, failed, unknown. executed counts the completed action prefix. For unknown, observe current application state before choosing another action.", "session_id desktop_epoch action_id", "session_id desktop_epoch lease_token action_id"},
		"computer.renew":         {"Renew your own desktop session before expires_at. Control sessions also require the current lease_token. Expired sessions require opening a new session and observing again.", "session_id desktop_epoch", "session_id desktop_epoch lease_token ttl_seconds"},
		"computer.close":         {"Close your desktop session and release its control authority and held input. Control sessions require the lease_token. Use this when the task finishes or you yield to a human.", "session_id desktop_epoch", "session_id desktop_epoch lease_token"},
		"computer.sessions":      {"List only the current caller's live desktop sessions and expiry times.", "", ""},
	}
	out := map[string]spec{}
	for name, d := range definitions {
		props := map[string]any{}
		for _, k := range strings.Fields(d[2]) {
			props[k] = str()
		}
		for _, k := range []string{"ttl_seconds", "max_width"} {
			if _, ok := props[k]; ok {
				props[k] = integer()
			}
		}
		if _, ok := props["ttl_seconds"]; ok {
			props["ttl_seconds"] = map[string]any{"type": "integer", "minimum": 10, "maximum": 600, "description": "Lease duration in seconds; omitted uses the configured default (120 seconds initially)."}
		}
		if _, ok := props["max_width"]; ok {
			props["max_width"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 8192, "description": "Maximum image width. Omitted or zero uses 1600; 1280 reduces network and model image cost."}
		}
		if _, ok := props["mode"]; ok {
			props["mode"] = map[string]any{"type": "string", "enum": []string{"observe", "control"}}
		}
		if _, ok := props["frame_id"]; ok {
			props["frame_id"] = map[string]any{"type": "string", "description": "A screenshot from this session, at most 60 seconds old. Layout or desktop changes invalidate it earlier."}
		}
		if _, ok := props["action_id"]; ok {
			props["action_id"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Caller-generated unique action identifier. The same ID is bound to the complete original request."}
		}
		if _, ok := props["observe_after"]; ok {
			props["observe_after"] = map[string]any{"type": "boolean"}
		}
		if _, ok := props["region"]; ok {
			props["region"] = region
		}
		if _, ok := props["actions"]; ok {
			props["actions"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": action}
		}
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if d[1] != "" {
			schema["required"] = strings.Fields(d[1])
		}
		accepted := map[string]any{}
		for k, v := range props {
			accepted[k] = v
		}
		for _, k := range ignoredArguments {
			accepted[k] = map[string]any{}
		}
		validation := map[string]any{"type": "object", "properties": accepted, "additionalProperties": false}
		if d[1] != "" {
			validation["required"] = strings.Fields(d[1])
		}
		compiler := jsonschema.NewCompiler()
		raw, _ := json.Marshal(validation)
		var normalized any
		_ = json.Unmarshal(raw, &normalized)
		if err := compiler.AddResource("urn:xmesh:"+name, normalized); err != nil {
			panic(err)
		}
		compiled, err := compiler.Compile("urn:xmesh:" + name)
		if err != nil {
			panic(err)
		}
		out[name] = spec{desc: d[0], schema: schema, compiled: compiled}
	}
	return out
}
