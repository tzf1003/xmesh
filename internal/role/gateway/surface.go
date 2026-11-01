package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// Surface selects how tools/list presents the mesh's tools (docs/contracts/gateway.md CTR-GW-08).
//
//	full    every device's tools, prefixed by the device name (default)
//	unified each tool once, with an optional "device" argument; only ListedGroups (and favorite backends) are listed
//	compact only the Gateway's own tools plus PinnedTools
type Surface struct {
	Mode         string
	ListedGroups []string
	PinnedTools  []string
	// MaxListed caps the tools listed in unified mode; 0 takes the default.
	MaxListed int
}

const (
	SurfaceFull    = "full"
	SurfaceUnified = "unified"
	SurfaceCompact = "compact"

	defaultMaxListed = 40
)

var defaultListedGroups = []string{"fs", "exec", "pty", "sys"}

func normalizeSurface(s Surface) Surface {
	switch s.Mode {
	case SurfaceUnified, SurfaceCompact:
	default:
		s.Mode = SurfaceFull
	}
	if s.ListedGroups == nil {
		s.ListedGroups = defaultListedGroups
	}
	if s.MaxListed <= 0 {
		s.MaxListed = defaultMaxListed
	}
	s.ListedGroups = slices.Clone(s.ListedGroups)
	s.PinnedTools = slices.Clone(s.PinnedTools)
	return s
}

// SetSurface applies a new gateway.tool_surface generation. Connected clients are
// told to read their tool lists again when it changed.
func (g *Gateway) SetSurface(s Surface) {
	next := normalizeSurface(s)
	if old := g.surface.Swap(&next); old != nil && reflect.DeepEqual(*old, next) {
		return
	}
	g.refreshViews()
}

// gatewayTool reports whether name is one of the Gateway's own tools.
func gatewayTool(name string) bool {
	switch name {
	case "devices.list", "tools.search", "tools.call", "session.set_defaults":
		return true
	}
	return false
}

// toolGroup is the part of a tool name before its first dot: fs, exec, browser, ida.
func toolGroup(name string) string {
	group, _, _ := strings.Cut(name, ".")
	return group
}

func schemaID(t *mcp.Tool) string {
	b, _ := json.Marshal(t.InputSchema)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

func schemaMap(t *mcp.Tool) map[string]any {
	b, _ := json.Marshal(t.InputSchema)
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		m = map[string]any{"type": "object"}
	}
	return m
}

var deviceProperty = map[string]any{"type": "string", "description": "目标设备名，见 devices.list；已用 session.set_defaults 设过默认设备时可省略。"}

// rewriteCall turns the two Gateway-level call forms into the ordinary prefixed
// call that the rest of the pipeline already handles, before audit and quota look
// at the name: tools.call, and, when the surface is not "full", an unprefixed tool
// name carrying a device argument. It returns a result when the request is
// rejected, and via when the call came through tools.call.
func (v *view) rewriteCall(call *mcp.CallToolRequest) (rejected *mcp.CallToolResult, via bool) {
	name := call.Params.Name
	if name == "tools.call" {
		var in struct {
			Device    string          `json:"device"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if len(call.Params.Arguments) == 0 || json.Unmarshal(call.Params.Arguments, &in) != nil || in.Tool == "" {
			return agent.Result(nil, toolerr.Args("tool")), false
		}
		tool := in.Tool
		if prefix, rest, ok := strings.Cut(tool, "/"); ok {
			if in.Device != "" && in.Device != prefix {
				return agent.Result(nil, toolerr.Args("device")), false
			}
			in.Device, tool = prefix, rest
		}
		if gatewayTool(tool) || tool == "" {
			return agent.Result(nil, toolerr.Args("tool")), false
		}
		if len(in.Arguments) == 0 || string(in.Arguments) == "null" {
			in.Arguments = json.RawMessage(`{}`)
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(in.Arguments, &probe) != nil || probe == nil {
			return agent.Result(nil, toolerr.Args("arguments")), false
		}
		if in.Device != "" {
			tool = in.Device + "/" + tool
		}
		call.Params.Name, call.Params.Arguments = tool, in.Arguments
		return nil, true
	}
	if v.g.surface.Load().Mode == SurfaceFull || strings.Contains(name, "/") || gatewayTool(name) {
		return nil, false
	}
	var args map[string]json.RawMessage
	if len(call.Params.Arguments) == 0 || json.Unmarshal(call.Params.Arguments, &args) != nil || args == nil {
		return nil, false
	}
	raw, ok := args["device"]
	if !ok {
		return nil, false
	}
	var device string
	if json.Unmarshal(raw, &device) != nil || device == "" {
		return agent.Result(nil, toolerr.Args("device")), false
	}
	delete(args, "device")
	rest, _ := json.Marshal(args)
	call.Params.Name, call.Params.Arguments = device+"/"+name, rest
	return nil, false
}

// missingDevice is the error for an unprefixed call with neither a device argument
// nor a session default; it lists the devices a retry could name.
func (v *view) missingDevice(tool string) *toolerr.Error {
	devices, _ := v.g.snapshot()
	online := []string{}
	for name, d := range devices {
		if d.online {
			online = append(online, v.g.prefix(name))
		}
	}
	sort.Strings(online)
	return toolerr.New("invalid_args", "缺少 device 参数，且未用 session.set_defaults 设置默认设备", "field", "device", "tool", tool, "online", online)
}

const attachSchemaLimit = 8 << 10

// attachSchema adds the target's input schema to an invalid_args error coming
// back through tools.call, so the model can correct the call without searching.
func (v *view) attachSchema(out *mcp.CallToolResult, full string) {
	if out == nil || !out.IsError {
		return
	}
	fields, ok := out.StructuredContent.(map[string]any)
	if !ok || fields["error"] != "invalid_args" {
		return
	}
	device, tool, e := mcpmux.Split(full, "/")
	if e != nil {
		return
	}
	if device == "local" && !v.g.opts.Ingress {
		device = v.g.opts.LocalPeer.Name
	}
	v.mu.Lock()
	var schema any
	if d := v.down[device]; d != nil {
		for _, t := range d.tools {
			if t.Name == tool {
				schema = t.InputSchema
			}
		}
	}
	v.mu.Unlock()
	if schema == nil {
		return
	}
	b, _ := json.Marshal(schema)
	if len(b) > attachSchemaLimit {
		props := []string{}
		if m, ok := schema.(map[string]any); ok {
			if p, ok := m["properties"].(map[string]any); ok {
				for k := range p {
					props = append(props, k)
				}
				sort.Strings(props)
			}
			fields = maps.Clone(fields)
			fields["parameters"] = props
			fields["required"] = m["required"]
		}
	} else {
		fields = maps.Clone(fields)
		fields["inputSchema"] = schema
	}
	enc, _ := json.Marshal(fields)
	out.StructuredContent = fields
	out.Content = []mcp.Content{&mcp.TextContent{Text: string(enc)}}
}

type toolVariant struct {
	tool    *mcp.Tool
	devices []string
}

// toolCatalog gathers every allowed tool of the online devices once per name and schema.
func (v *view) toolCatalog() map[string]map[string]*toolVariant {
	out := map[string]map[string]*toolVariant{}
	v.mu.Lock()
	defer v.mu.Unlock()
	for device, d := range v.down {
		if d.dead {
			continue
		}
		for _, t := range d.tools {
			if !v.allowed(device, t.Name, "") {
				continue
			}
			id := schemaID(t)
			if out[t.Name] == nil {
				out[t.Name] = map[string]*toolVariant{}
			}
			if out[t.Name][id] == nil {
				out[t.Name][id] = &toolVariant{tool: t}
			}
			out[t.Name][id].devices = append(out[t.Name][id].devices, v.g.prefix(device))
		}
	}
	return out
}

// majority picks the schema shared by the most devices; ties go to the lowest id.
func majority(variants map[string]*toolVariant) (string, *toolVariant) {
	best, id := (*toolVariant)(nil), ""
	for k, c := range variants {
		if best == nil || len(c.devices) > len(best.devices) || len(c.devices) == len(best.devices) && k < id {
			best, id = c, k
		}
	}
	return id, best
}

// unifiedTools lists each selected tool once, with an optional device argument
// (CTR-GW-08). Tools that declare a device argument themselves are left out; they
// stay reachable through tools.call.
func (v *view) unifiedTools(s Surface) []*mcp.Tool {
	catalog := v.toolCatalog()
	v.mu.Lock()
	favorites := map[string]bool{}
	for _, d := range v.down {
		if d.dead {
			continue
		}
		for _, t := range d.tools {
			if !builtin(t.Name) && favorite(d.info, t.Name) {
				favorites[toolGroup(t.Name)] = true
			}
		}
	}
	v.mu.Unlock()
	listed := map[string]bool{}
	size := map[string]int{}
	for name := range catalog {
		group := toolGroup(name)
		size[group]++
		switch {
		case slices.Contains(s.PinnedTools, name):
			listed[name] = true
		case s.Mode == SurfaceUnified && slices.Contains(s.ListedGroups, group):
			listed[name] = true
		}
	}
	if s.Mode == SurfaceUnified {
		extra := []string{}
		for group := range favorites {
			if !slices.Contains(s.ListedGroups, group) {
				extra = append(extra, group)
			}
		}
		sort.Slice(extra, func(i, j int) bool {
			if size[extra[i]] != size[extra[j]] {
				return size[extra[i]] < size[extra[j]]
			}
			return extra[i] < extra[j]
		})
		total := len(listed)
		for _, group := range extra {
			if total+size[group] > s.MaxListed {
				continue
			}
			for name := range catalog {
				if toolGroup(name) == group {
					listed[name] = true
				}
			}
			total += size[group]
		}
	}
	names := []string{}
	for name := range listed {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := []*mcp.Tool{}
	for _, name := range names {
		_, c := majority(catalog[name])
		schema := schemaMap(c.tool)
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		if _, own := props["device"]; own {
			continue
		}
		props = maps.Clone(props)
		props["device"] = deviceProperty
		schema["properties"] = props
		t := *c.tool
		t.InputSchema = schema
		tools = append(tools, &t)
	}
	return tools
}

// gatewayToolVisible says whether tools/list shows one of the Gateway's own
// tools. tools.call carries no scope of its own: its target is authorized at
// call time, so it is listed for any client that holds at least one scope.
func (v *view) gatewayToolVisible(name string) bool {
	if name == "tools.call" {
		return v.p.Local || len(v.p.Scopes) > 0
	}
	return v.allowed("", name, "")
}
