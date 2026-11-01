package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"slices"
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
			Description: "查找工具并按需取得 schema。device 给定时只在该设备的完整工具表里找（含默认 tools/list 没有列出的工具），返回带前缀的 name；device 省略时在所有在线、有权限的设备上找，按工具与 schema 去重，返回 tool、description、schema_id 与 devices。pattern 为大小写不敏感的子串（* 为全部），group 按组筛选（如 fs、browser），names 按精确名取，三者至少给一个。detail 为 brief（不含 inputSchema）或 schema；省略 device 时缺省 brief。找到后用 tools.call 调用。需要 devices:read。",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"device":  map[string]any{"type": "string", "description": "节点名，或 local（本机）；省略表示所有在线设备。"},
					"pattern": map[string]any{"type": "string", "description": "大小写不敏感的子串，不是正则；匹配工具名与描述。传 * 列出全部工具。"},
					"group":   map[string]any{"type": "string", "description": "工具组，即工具名第一个 . 之前的部分，如 fs、computer、browser。"},
					"names":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "精确的工具名，带或不带设备前缀均可。"},
					"detail":  map[string]any{"enum": []string{"brief", "schema"}, "description": "brief 只返回名字与说明；schema 同时返回 inputSchema。"},
				},
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name:        "tools.call",
			Description: "调用任意设备上的工具，语义与直接调用该工具完全一致，结果原样返回。用于没有列在 tools/list 里的工具：先用 tools.search 取得名字和 schema。device 省略时用 session.set_defaults 设置的默认设备。参数错误时返回里会带该工具的 inputSchema。",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"tool"},
				"properties": map[string]any{
					"device":    map[string]any{"type": "string", "description": "目标设备名；省略则用默认设备。"},
					"tool":      map[string]any{"type": "string", "description": "工具名，如 browser.open；也可写成 设备/工具。"},
					"arguments": map[string]any{"type": "object", "description": "传给该工具的参数，按它的 inputSchema 填写。"},
				},
			},
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
			row := map[string]any{"id": name, "online": device.online, "os": device.peer.OS, "tags": device.peer.Tags, "backends": []string{}, "groups": map[string]int{}, "load": map[string]any{}, "stale": true}
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
				info := []map[string]any{}
				if list, ok := d.info["backends"].([]any); ok {
					for _, item := range list {
						b, ok := item.(map[string]any)
						bname, _ := b["name"].(string)
						if !ok || bname == "" {
							continue
						}
						count := 0
						for _, tool := range d.tools {
							if toolGroup(tool.Name) == bname && v.allowed(name, tool.Name, "") {
								count++
							}
						}
						entry := map[string]any{"name": bname, "policy": b["policy"], "favorite": b["favorite"] == true, "tools": count}
						if desc, _ := b["desc"].(string); desc != "" {
							entry["desc"] = desc
						}
						info = append(info, entry)
					}
				}
				sort.Slice(info, func(i, j int) bool { return info[i]["name"].(string) < info[j]["name"].(string) })
				row["backend_info"] = info
				groups := map[string]int{}
				for _, tool := range d.tools {
					if v.allowed(name, tool.Name, "") {
						groups[toolGroup(tool.Name)]++
					}
				}
				row["groups"] = groups
				row["stale"] = !d.failedSince.IsZero()
				if limits, ok := d.info["limits"].(map[string]any); ok {
					row["load"] = map[string]any{"pty_in_use": limits["pty_in_use"], "pty_max": limits["pty_max_sessions"], "exec_shared_in_use": limits["exec_shared_in_use"]}
				}
			}
			rows = append(rows, row)
		}
		return agent.Result(map[string]any{"devices": rows}, nil)
	case "tools.search":
		return v.search(ctx, devices, args)
	case "tools.call":
		// tools.call is rewritten to its target before dispatch; reaching here means it was not.
		return agent.Result(nil, toolerr.Args("tool"))
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
		previousDevice := v.defaultDeviceLocked()
		for key, raw := range args {
			if string(raw) == "null" {
				delete(v.defaults, key)
			} else {
				v.defaults[key] = raw
			}
		}
		deviceChanged := previousDevice != v.defaultDeviceLocked()
		v.mu.Unlock()
		if deviceChanged {
			v.refresh()
		}
		return agent.Result(map[string]any{"ok": true}, nil)
	}
	return agent.Result(nil, toolerr.New("not_found", "工具不存在", "name", name))
}

// search implements tools.search (CTR-GW-02). With a device it keeps the
// per-device answer, names prefixed; without one it answers across the online
// devices, one row per tool and schema.
func (v *view) search(ctx context.Context, devices map[string]device, args map[string]json.RawMessage) *mcp.CallToolResult {
	var device, pattern, group, detail string
	var names []string
	for key, dst := range map[string]*string{"device": &device, "pattern": &pattern, "group": &group, "detail": &detail} {
		if raw, ok := args[key]; ok && string(raw) != "null" && json.Unmarshal(raw, dst) != nil {
			return agent.Result(nil, toolerr.Args(key))
		}
	}
	if raw, ok := args["names"]; ok && string(raw) != "null" && json.Unmarshal(raw, &names) != nil {
		return agent.Result(nil, toolerr.Args("names"))
	}
	for key := range args {
		switch key {
		case "device", "pattern", "group", "names", "detail":
		default:
			return agent.Result(nil, toolerr.Args(key))
		}
	}
	if detail != "" && detail != "brief" && detail != "schema" {
		return agent.Result(nil, toolerr.Args("detail"))
	}
	if pattern == "" && group == "" && len(names) == 0 {
		return agent.Result(nil, toolerr.Args("pattern"))
	}
	match := func(full, tool, description string) bool {
		if group != "" && toolGroup(tool) != group {
			return false
		}
		if len(names) > 0 && !slices.Contains(names, tool) && !slices.Contains(names, full) {
			return false
		}
		return pattern == "" || pattern == "*" || strings.Contains(strings.ToLower(full+" "+description), strings.ToLower(pattern))
	}
	if device != "" {
		if device == "local" && !v.g.opts.Ingress {
			device = v.g.opts.LocalPeer.Name
		}
		peer, exists := devices[device]
		if !exists || !peer.online {
			return agent.Result(nil, toolerr.New("not_found", "设备不存在", "name", device))
		}
		v.waitReady(ctx, device)
		v.mu.Lock()
		defer v.mu.Unlock()
		rows := []map[string]any{}
		truncated := false
		if d := v.down[device]; d != nil {
			for _, tool := range d.tools {
				if !v.allowed(device, tool.Name, "") {
					continue
				}
				full := v.g.prefix(device) + "/" + tool.Name
				if !match(full, tool.Name, tool.Description) {
					continue
				}
				row := map[string]any{"name": full, "description": tool.Description}
				if tool.InputSchema != nil && detail != "brief" {
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
		return agent.Result(map[string]any{"tools": rows, "truncated": truncated}, nil)
	}
	v.waitOthers(ctx)
	limit := searchBriefLimit
	if detail == "schema" {
		limit = searchSchemaLimit
	}
	catalog := v.toolCatalog()
	keys := []string{}
	for name := range catalog {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	rows := []map[string]any{}
	truncated := false
	for _, name := range keys {
		ids := []string{}
		for id := range catalog[name] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			c := catalog[name][id]
			if !match(name, name, c.tool.Description) {
				continue
			}
			if len(rows) >= limit {
				truncated = true
				break
			}
			sort.Strings(c.devices)
			row := map[string]any{"tool": name, "description": c.tool.Description, "schema_id": id, "devices": c.devices}
			if detail == "schema" && c.tool.InputSchema != nil {
				row["inputSchema"] = c.tool.InputSchema
			}
			if c.tool.Annotations != nil {
				row["annotations"] = c.tool.Annotations
			}
			rows = append(rows, row)
		}
		if truncated {
			break
		}
	}
	return agent.Result(map[string]any{"tools": rows, "truncated": truncated}, nil)
}

const (
	searchBriefLimit  = 200
	searchSchemaLimit = 25
)
