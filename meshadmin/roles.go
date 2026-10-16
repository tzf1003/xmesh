package meshadmin

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Grant mirrors one agent.authz entry (CTR-AUTHZ-01).
type Grant struct {
	Backends []string `json:"backends" yaml:"backends"`
	Tools    []string `json:"tools" yaml:"tools"`
}

// NodeInfo is what xmesh.yaml tells us about one node. It is optional input:
// without it the tag checks fall back to what the control plane shows.
type NodeInfo struct {
	Name  string           `json:"name" yaml:"name"`
	Roles []string         `json:"roles" yaml:"roles"`
	Authz map[string]Grant `json:"authz,omitempty" yaml:"-"`
}

// LoadNodeInfo reads node.name, node.roles and agent.authz from an xmesh.yaml.
// It ignores every other key and never reads secrets: the file only holds paths.
func LoadNodeInfo(path string) (NodeInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return NodeInfo{}, err
	}
	var doc struct {
		Node  NodeInfo `yaml:"node"`
		Agent struct {
			Authz map[string]Grant `yaml:"authz"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return NodeInfo{}, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	if doc.Node.Name == "" {
		return NodeInfo{}, fmt.Errorf("%w: %s has no node.name", ErrInvalid, path)
	}
	n := doc.Node
	n.Authz = doc.Agent.Authz
	return n, nil
}

// Role is an xmesh role as inferred from tags.
type Role string

const (
	RoleAgent   Role = "agent"
	RoleIngress Role = "ingress"
	RoleAdmin   Role = "admin"
	RolePeer    Role = "peer" // trusted caller, usually a Gateway on a user device
)

// RolesFromTags maps tags to xmesh roles (sorted, unique).
func RolesFromTags(tags []string) []Role {
	set := map[Role]bool{}
	for _, t := range tags {
		switch {
		case t == TagDevice:
			set[RoleAgent] = true
		case t == TagIngress:
			set[RoleIngress] = true
		case t == TagAdmin:
			set[RoleAdmin] = true
		case t == TagPeer, t == "tag:cloud", strings.HasPrefix(t, "tag:gw-"):
			set[RolePeer] = true
		}
	}
	out := make([]Role, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Severity grades an Issue.
type Severity string

const (
	SevInfo    Severity = "info"
	SevWarning Severity = "warning"
	SevError   Severity = "error"
)

// Issue is one finding of the tag and key checks.
type Issue struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

// DeviceReport is one control-plane device with its xmesh view.
type DeviceReport struct {
	Device Device   `json:"device"`
	Roles  []Role   `json:"roles"`
	Node   string   `json:"node,omitempty"` // matching xmesh node name, when a NodeInfo matched
	Issues []Issue  `json:"issues"`
	Tags   []string `json:"tags"`
}

// MatchNode finds the NodeInfo for a device by hostname or the first DNS label.
func MatchNode(d Device, nodes []NodeInfo) *NodeInfo {
	label := d.Name
	if i := strings.IndexByte(label, '.'); i >= 0 {
		label = label[:i]
	}
	for i := range nodes {
		n := &nodes[i]
		if strings.EqualFold(n.Name, d.Hostname) || strings.EqualFold(n.Name, label) || strings.EqualFold(n.Name, d.Name) {
			return n
		}
	}
	return nil
}

// roleTags lists the tags that satisfy a configured node role.
var roleTags = map[string][]string{
	"agent":   {TagDevice},
	"ingress": {TagIngress},
	"gateway": {TagPeer, "tag:cloud"},
}

// Analyze checks every device: missing tags, tags that disagree with the role the
// node's own config declares, unauthorised nodes and node keys that are expired
// or about to expire. now is injected for tests.
func Analyze(devices []Device, nodes []NodeInfo, now time.Time) []DeviceReport {
	out := make([]DeviceReport, 0, len(devices))
	for _, d := range devices {
		r := DeviceReport{Device: d, Roles: RolesFromTags(d.Tags), Tags: nonNil(d.Tags), Issues: []Issue{}}
		add := func(s Severity, code, msg string) {
			r.Issues = append(r.Issues, Issue{Severity: s, Code: code, Message: msg})
		}
		if len(d.Tags) == 0 {
			add(SevWarning, "no-tags", "node has no tags; agent.authz and the network policy cannot recognise it (give it tag:device, tag:peer, ...)")
		}
		if !d.Authorized {
			add(SevWarning, "unauthorized", "node is not authorised by the control plane")
		}
		if !d.Expires.IsZero() {
			switch left := d.Expires.Sub(now); {
			case left <= 0:
				add(SevError, "key-expired", "node key expired "+d.Expires.Format(time.RFC3339))
			case left < 7*24*time.Hour:
				add(SevWarning, "key-expiring", "node key expires "+d.Expires.Format(time.RFC3339))
			}
		}
		if n := MatchNode(d, nodes); n != nil {
			r.Node = n.Name
			declared := map[string]bool{}
			for _, role := range n.Roles {
				declared[role] = true
				want, ok := roleTags[role]
				if !ok {
					continue
				}
				if !hasAny(d.Tags, want) {
					add(SevError, "missing-role-tag", fmt.Sprintf("xmesh.yaml declares role %q but the node lacks %s (it will not appear in discovery or will be denied)", role, strings.Join(want, " or ")))
				}
			}
			if !declared["agent"] && hasAny(d.Tags, []string{TagDevice}) {
				add(SevWarning, "unexpected-device-tag", "node carries tag:device but xmesh.yaml does not enable the agent role")
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device.Name < out[j].Device.Name })
	return out
}

func hasAny(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}
