package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"sort"
	"strings"
)

// controlTools describes the three tools the Gateway itself serves, with the
// parameters CTR-GW-02 and CTR-GW-03 define. Models only see these schemas.
func controlTools() []*mcp.Tool {
	nullable := func(kind string) []string { return []string{kind, "null"} }
	return []*mcp.Tool{
		{
			Name:        "devices.list",
			Description: "列出网络中的设备：id（节点名，也是其他工具的前缀）、os、online、tags、backends 与 load（终端和 exec 占用）。无参数。离线设备最多保留 24 小时，online 为 false；从未成功读取过信息的新设备带 stale: true。需要 devices:read。",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:        "tools.search",
			Description: "在某台设备的完整工具表中按子串查找工具，包括默认 tools/list 没有列出的后端工具。返回带前缀的 name、description 与 inputSchema，最多 100 条，truncated 为 true 表示结果被截断。pattern 传 * 列出全部工具。需要 devices:read。",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"device", "pattern"},
				"properties": map[string]any{
					"device":  map[string]any{"type": "string", "description": "节点名，或 local（本机）。"},
					"pattern": map[string]any{"type": "string", "minLength": 1, "description": "大小写不敏感的子串，不是正则；匹配工具名与描述。传 * 列出全部工具。"},
				},
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:        "session.set_defaults",
			Description: "设置当前 MCP 连接的默认值，至少给出一项：device 让不带设备前缀的工具名走该设备；cwd 与 env 会补进带有这两个参数的内置工具（fs.*、exec.run、pty.open）的调用，调用方显式传入的值优先，后端工具不受影响。传 null 清除该项，未提交的项保持不变。cwd 只在目标设备上校验。只影响当前连接。",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"minProperties":        1,
				"properties": map[string]any{
					"device": map[string]any{"type": nullable("string"), "description": "默认设备：节点名或 local，必须在线。"},
					"cwd":    map[string]any{"type": nullable("string"), "description": "默认工作目录，原样保存，由目标设备在调用时判定。"},
					"env":    map[string]any{"type": nullable("object"), "additionalProperties": map[string]any{"type": "string"}, "description": "默认环境变量，值为字符串；整体替换先前提交的 env。"},
				},
			},
		},
	}
}

func (v *view) control(ctx context.Context, name string, raw json.RawMessage) *mcp.CallToolResult {
	if !v.allowed("", name, "") {
		return agent.Result(nil, toolerr.New("unauthorized", "需要设备发现权限", "scope", "devices:read", "tool", name))
	}
	args := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if e := json.Unmarshal(raw, &args); e != nil || args == nil {
			return agent.Result(nil, toolerr.Args("arguments"))
		}
	}
	devices, _ := v.g.snapshot()
	switch name {
	case "devices.list":
		rows := []map[string]any{}
		keys := []string{}
		for name := range devices {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		v.mu.Lock()
		defer v.mu.Unlock()
		for _, name := range keys {
			device := devices[name]
			row := map[string]any{"id": name, "online": device.online, "os": device.peer.OS, "tags": device.peer.Tags, "backends": []string{}, "load": map[string]any{}, "stale": true}
			if d := v.down[name]; d != nil && d.info != nil {
				row["os"] = d.info["os"]
				backends := []string{}
				if list, ok := d.info["backends"].([]any); ok {
					for _, item := range list {
						if b, ok := item.(map[string]any); ok {
							if name, ok := b["name"].(string); ok {
								backends = append(backends, name)
							}
						}
					}
				}
				sort.Strings(backends)
				row["backends"] = backends
				row["stale"] = !d.failedSince.IsZero()
				if limits, ok := d.info["limits"].(map[string]any); ok {
					row["load"] = map[string]any{"pty_in_use": limits["pty_in_use"], "pty_max": limits["pty_max_sessions"], "exec_shared_in_use": limits["exec_shared_in_use"]}
				}
			}
			rows = append(rows, row)
		}
		return agent.Result(map[string]any{"devices": rows}, nil)
	case "tools.search":
		var device, pattern string
		if json.Unmarshal(args["device"], &device) != nil || json.Unmarshal(args["pattern"], &pattern) != nil || pattern == "" {
			return agent.Result(nil, toolerr.Args("device/pattern"))
		}
		if device == "local" && !v.g.opts.Ingress {
			device = v.g.opts.LocalPeer.Name
		}
		peer, exists := devices[device]
		if !exists || !peer.online {
			return agent.Result(nil, toolerr.New("not_found", "设备不存在", "name", device))
		}
		v.waitReady(ctx, device)
		v.mu.Lock()
		d := v.down[device]
		rows := []map[string]any{}
		truncated := false
		if d != nil {
			for _, tool := range d.tools {
				if !v.allowed(device, tool.Name, "") {
					continue
				}
				full := v.g.prefix(device) + "/" + tool.Name
				if pattern == "*" || strings.Contains(strings.ToLower(full+" "+tool.Description), strings.ToLower(pattern)) {
					row := map[string]any{"name": full, "description": tool.Description}
					if tool.InputSchema != nil {
						row["inputSchema"] = tool.InputSchema
					}
					if tool.Annotations != nil {
						row["annotations"] = tool.Annotations
					}
					rows = append(rows, row)
					if len(rows) > 100 {
						rows = rows[:100]
						truncated = true
						break
					}
				}
			}
		}
		v.mu.Unlock()
		return agent.Result(map[string]any{"tools": rows, "truncated": truncated}, nil)
	case "session.set_defaults":
		if len(args) == 0 {
			return agent.Result(nil, toolerr.Args("arguments"))
		}
		for key, raw := range args {
			if key != "device" && key != "cwd" && key != "env" {
				return agent.Result(nil, toolerr.Args(key))
			}
			if string(raw) == "null" {
				continue
			}
			switch key {
			case "device":
				var device string
				if json.Unmarshal(raw, &device) != nil {
					return agent.Result(nil, toolerr.Args(key))
				}
				if device == "local" && !v.g.opts.Ingress {
					device = v.g.opts.LocalPeer.Name
				}
				if d, ok := devices[device]; !ok || !d.online {
					return agent.Result(nil, toolerr.Args(key))
				}
			case "cwd":
				var text string
				if json.Unmarshal(raw, &text) != nil {
					return agent.Result(nil, toolerr.Args(key))
				}
			case "env":
				var env map[string]string
				if json.Unmarshal(raw, &env) != nil {
					return agent.Result(nil, toolerr.Args(key))
				}
			}
		}
		v.mu.Lock()
		for key, raw := range args {
			if string(raw) == "null" {
				delete(v.defaults, key)
			} else {
				v.defaults[key] = raw
			}
		}
		v.mu.Unlock()
		return agent.Result(map[string]any{"ok": true}, nil)
	}
	return agent.Result(nil, toolerr.New("not_found", "工具不存在", "name", name))
}
