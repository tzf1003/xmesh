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
	region := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"x", "y", "width", "height"}, "properties": map[string]any{"x": integer(), "y": integer(), "width": integer(), "height": integer()}}
	actionProps := map[string]any{}
	for _, k := range strings.Fields("type button text") {
		actionProps[k] = str()
	}
	for _, k := range strings.Fields("x y to_x to_y clicks delta_x delta_y duration_ms") {
		actionProps[k] = integer()
	}
	actionProps["keys"] = map[string]any{"type": "array", "maxItems": 8, "items": str()}
	action := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": actionProps}
	definitions := map[string][3]string{
		"computer.info":          {"查询用户桌面和系统权限", "", ""},
		"computer.open":          {"打开观察或排他控制会话", "mode", "desktop_id mode ttl_seconds"},
		"computer.observe":       {"获取桌面截图与图片像素坐标信息", "session_id desktop_epoch", "session_id desktop_epoch lease_token display_id max_width region"},
		"computer.act":           {"执行有限桌面动作，超时后查询 action_status", "session_id desktop_epoch lease_token action_id actions", "session_id desktop_epoch lease_token action_id frame_id actions observe_after"},
		"computer.action_status": {"查询桌面动作的执行状态", "session_id desktop_epoch action_id", "session_id desktop_epoch lease_token action_id"},
		"computer.renew":         {"续期所属桌面会话", "session_id desktop_epoch", "session_id desktop_epoch lease_token ttl_seconds"},
		"computer.close":         {"关闭所属桌面会话并释放输入", "session_id desktop_epoch", "session_id desktop_epoch lease_token"},
		"computer.sessions":      {"列出所属桌面会话", "", ""},
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
