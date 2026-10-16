package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

// callerPart makes a session id usable as the second half of a caller id.
func callerPart(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			b[i] = '_'
		}
	}
	if len(b) > 128 {
		b = b[:128]
	}
	if len(b) == 0 {
		return "_"
	}
	return string(b)
}

func (g *Gateway) nodeID() string {
	if g.opts.NodeID != "" {
		return g.opts.NodeID
	}
	return g.opts.NodeName
}

// oauthAuditID hides the grant family behind a short digest; the audit log only
// needs to correlate calls from one OAuth session.
func oauthAuditID(session string) string {
	if session == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:8])
}

// auditIdentity names the device and caller of a tool call (CTR-AUD-01). The
// caller is the identity the target Agent records: this node's network id and
// the downstream MCP session. Gateway-local tools, and calls with no downstream
// connection yet, fall back to this node and the upstream session.
func (v *view) auditIdentity(tool, upstream string) (device, caller string) {
	g := v.g
	device = g.opts.NodeName
	caller = g.nodeID() + "/" + callerPart(upstream)
	prefix, _, ok := strings.Cut(tool, "/")
	if !ok || prefix == "" {
		return device, caller
	}
	device = prefix
	name := prefix
	if prefix == "local" && !g.opts.Ingress {
		name = g.opts.LocalPeer.Name
		device = name
	}
	v.mu.Lock()
	d := v.down[name]
	v.mu.Unlock()
	if d != nil && d.bridge != nil && d.bridge.Session != nil {
		if id := d.bridge.Session.ID(); id != "" {
			caller = g.nodeID() + "/" + callerPart(id)
		}
	}
	return device, caller
}

// auditUnrouted records a progress notification that matched no pending call
// (CTR-MUX-02). It is called by the downstream bridge, not a tool call.
func (v *view) auditUnrouted(device string) {
	g := v.g
	if g.opts.Audit == nil {
		return
	}
	v.mu.Lock()
	var upstream string
	if v.session != nil {
		upstream = v.session.ID()
	}
	v.mu.Unlock()
	_, caller := v.auditIdentity(device+"/progress", upstream)
	_ = g.opts.Audit.Event("progress_unrouted", caller, v.oauthAudit, device, "notifications/progress")
}

// declares reports whether the named tool's input schema has the property.
func declares(tools []*mcp.Tool, tool, property string) bool {
	for _, t := range tools {
		if t.Name != tool {
			continue
		}
		b, e := json.Marshal(t.InputSchema)
		if e != nil {
			return false
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		return json.Unmarshal(b, &schema) == nil && schema.Properties[property] != nil
	}
	return false
}

// meshDraining recognises the Agent's drain refusal (CTR-GW-05).
func meshDraining(out *mcp.CallToolResult) bool {
	b, e := json.Marshal(out.StructuredContent)
	if e != nil {
		return false
	}
	var v struct {
		Error string `json:"error"`
		Rule  string `json:"rule"`
	}
	return json.Unmarshal(b, &v) == nil && v.Error == "policy_denied" && v.Rule == "mesh.draining"
}
