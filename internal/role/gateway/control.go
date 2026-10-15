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
				if strings.Contains(strings.ToLower(full+" "+tool.Description), strings.ToLower(pattern)) {
					rows = append(rows, map[string]any{"name": full, "description": tool.Description})
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
