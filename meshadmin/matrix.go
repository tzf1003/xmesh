package meshadmin

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Decision is a tri-state: the evaluator reports Unknown for policy constructs
// it cannot resolve (autogroup:admin, node attributes, ...) rather than guess.
type Decision string

const (
	Allow   Decision = "allow"
	Deny    Decision = "deny"
	Unknown Decision = "unknown"
)

// Cell answers "may Src call Dst" for both authorisation layers.
type Cell struct {
	Src         string   `json:"src"`
	Dst         string   `json:"dst"`
	Network     Decision `json:"network"`
	NetworkNote string   `json:"network_note,omitempty"`
	Tools       Decision `json:"tools"`
	// AllowedTools and Backends are the union of the dst node's agent.authz
	// entries for the tags src carries.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Backends     []string `json:"backends,omitempty"`
	// Mismatch is set when the layers disagree: one lets the call through and the
	// other cannot. It is the cell the user must fix.
	Mismatch string `json:"mismatch,omitempty"`
}

// Matrix is the merged view (network layer from the control-plane policy, tool
// layer from each node's agent.authz).
type Matrix struct {
	Port  int    `json:"port"`
	Cells []Cell `json:"cells"`
}

type policyDoc struct {
	ACLs []struct {
		Action string   `json:"action"`
		Proto  string   `json:"proto"`
		Src    []string `json:"src"`
		Dst    []string `json:"dst"`
	} `json:"acls"`
	Grants []struct {
		Src []string `json:"src"`
		Dst []string `json:"dst"`
		IP  []string `json:"ip"`
	} `json:"grants"`
	Groups map[string][]string `json:"groups"`
	Hosts  map[string]string   `json:"hosts"`
}

// BuildMatrix evaluates, for every ordered pair (src, dst) where dst is an
// agent, whether the network policy admits tcp/port and what dst's agent.authz
// lets src's tags call. nodes supplies the tool layer; a dst without a matching
// NodeInfo gets Tools=unknown.
func BuildMatrix(policyText string, devices []Device, nodes []NodeInfo, port int) (Matrix, error) {
	if port == 0 {
		port = AgentPort
	}
	var pol policyDoc
	havePolicy := strings.TrimSpace(policyText) != ""
	if havePolicy {
		std, err := StandardJSON(policyText)
		if err != nil {
			return Matrix{}, err
		}
		if err := json.Unmarshal(std, &pol); err != nil {
			return Matrix{}, fmt.Errorf("%w: policy shape: %v", ErrInvalid, err)
		}
	}
	m := Matrix{Port: port}
	for _, dst := range devices {
		node := MatchNode(dst, nodes)
		isAgent := hasAny(dst.Tags, []string{TagDevice})
		if node != nil {
			for _, r := range node.Roles {
				if r == "agent" {
					isAgent = true
				}
			}
		}
		if !isAgent {
			continue
		}
		for _, src := range devices {
			if src.ID == dst.ID {
				continue
			}
			c := Cell{Src: deviceLabel(src), Dst: deviceLabel(dst)}
			if havePolicy && (len(pol.ACLs) > 0 || len(pol.Grants) > 0) {
				c.Network, c.NetworkNote = pol.allows(src, dst, port)
			} else {
				c.Network, c.NetworkNote = Unknown, "policy has no acls or grants"
			}
			if node == nil || node.Authz == nil {
				c.Tools = Unknown
			} else {
				c.AllowedTools, c.Backends = authzFor(node.Authz, src.Tags)
				c.Tools = Deny
				if len(c.AllowedTools) > 0 {
					c.Tools = Allow
				}
			}
			switch {
			case c.Network == Allow && c.Tools == Deny:
				c.Mismatch = fmt.Sprintf("network allows tcp/%d but %s's agent.authz has no usable entry for %s", port, c.Dst, tagList(src.Tags))
			case c.Network == Deny && c.Tools == Allow:
				c.Mismatch = fmt.Sprintf("agent.authz on %s grants tools to %s but the network policy blocks tcp/%d", c.Dst, tagList(src.Tags), port)
			}
			m.Cells = append(m.Cells, c)
		}
	}
	sort.Slice(m.Cells, func(i, j int) bool {
		if m.Cells[i].Dst != m.Cells[j].Dst {
			return m.Cells[i].Dst < m.Cells[j].Dst
		}
		return m.Cells[i].Src < m.Cells[j].Src
	})
	return m, nil
}

func deviceLabel(d Device) string {
	if d.Hostname != "" {
		return d.Hostname
	}
	return d.Name
}

func tagList(tags []string) string {
	if len(tags) == 0 {
		return "an untagged node"
	}
	return strings.Join(tags, ", ")
}

// authzFor merges the agent.authz entries of every tag the caller carries.
func authzFor(authz map[string]Grant, tags []string) (tools, backends []string) {
	seenT, seenB := map[string]bool{}, map[string]bool{}
	for _, t := range tags {
		g, ok := authz[t]
		if !ok {
			continue
		}
		for _, x := range g.Tools {
			if !seenT[x] {
				seenT[x] = true
				tools = append(tools, x)
			}
		}
		for _, x := range g.Backends {
			if !seenB[x] {
				seenB[x] = true
				backends = append(backends, x)
			}
		}
	}
	sort.Strings(tools)
	sort.Strings(backends)
	return
}

// allows evaluates acls (accept, tcp) and grants for src -> dst:port.
func (p policyDoc) allows(src, dst Device, port int) (Decision, string) {
	unknown := false
	for i, r := range p.ACLs {
		if r.Action != "accept" {
			continue
		}
		if r.Proto != "" && r.Proto != "tcp" && r.Proto != "6" {
			continue
		}
		sm, sk := p.anyAlias(r.Src, src, src)
		if !sm && sk {
			continue
		}
		if !sm && !sk {
			unknown = true
			continue
		}
		for _, entry := range r.Dst {
			alias, ports := splitDst(entry)
			dm, dk := p.matchAlias(alias, dst, src, 0)
			if !dm {
				if !dk {
					unknown = true
				}
				continue
			}
			if portsCover(ports, port) {
				return Allow, fmt.Sprintf("acls[%d]: %s", i, entry)
			}
		}
	}
	for i, g := range p.Grants {
		sm, sk := p.anyAlias(g.Src, src, src)
		if !sm {
			if !sk {
				unknown = true
			}
			continue
		}
		covered := false
		for _, ip := range g.IP {
			if ipCovers(ip, port) {
				covered = true
			}
		}
		if !covered {
			continue
		}
		for _, d := range g.Dst {
			dm, dk := p.matchAlias(d, dst, src, 0)
			if dm {
				return Allow, fmt.Sprintf("grants[%d]: %s", i, d)
			}
			if !dk {
				unknown = true
			}
		}
	}
	if unknown {
		return Unknown, "a rule uses an alias xmesh cannot resolve (for example autogroup:admin)"
	}
	return Deny, "no rule admits it"
}

// anyAlias returns (matched, known). known=false means the alias list contained
// something that could not be resolved and nothing else matched.
func (p policyDoc) anyAlias(aliases []string, d, src Device) (bool, bool) {
	known := true
	for _, a := range aliases {
		m, k := p.matchAlias(a, d, src, 0)
		if m {
			return true, true
		}
		if !k {
			known = false
		}
	}
	return false, known
}

// matchAlias reports (matches, known).
func (p policyDoc) matchAlias(alias string, d, src Device, depth int) (bool, bool) {
	if depth > 8 {
		return false, false
	}
	switch {
	case alias == "*":
		return true, true
	case strings.HasPrefix(alias, "tag:"):
		return hasAny(d.Tags, []string{alias}), true
	case alias == "autogroup:member":
		return len(d.Tags) == 0, true
	case alias == "autogroup:tagged":
		return len(d.Tags) > 0, true
	case alias == "autogroup:self":
		return len(d.Tags) == 0 && len(src.Tags) == 0 && d.User != "" && d.User == src.User, true
	case strings.HasPrefix(alias, "autogroup:"):
		return false, false
	case strings.HasPrefix(alias, "group:"):
		members, ok := p.Groups[alias]
		if !ok {
			return false, true
		}
		known := true
		for _, mem := range members {
			m, k := p.matchAlias(mem, d, src, depth+1)
			if m {
				return true, true
			}
			known = known && k
		}
		return false, known
	case strings.Contains(alias, "@"):
		if len(d.Tags) > 0 {
			return false, true
		}
		a, u := strings.ToLower(alias), strings.ToLower(d.User)
		return u != "" && (a == u || a == u+"@" || strings.TrimSuffix(a, "@") == u), true
	}
	if target, ok := p.Hosts[alias]; ok {
		return addrsIn(target, d.Addresses), true
	}
	if _, err := netip.ParsePrefix(alias); err == nil {
		return addrsIn(alias, d.Addresses), true
	}
	if _, err := netip.ParseAddr(alias); err == nil {
		return addrsIn(alias, d.Addresses), true
	}
	return false, false
}

func addrsIn(target string, addrs []string) bool {
	var pfx netip.Prefix
	if p, err := netip.ParsePrefix(target); err == nil {
		pfx = p
	} else if a, err := netip.ParseAddr(target); err == nil {
		pfx = netip.PrefixFrom(a, a.BitLen())
	} else {
		return false
	}
	for _, s := range addrs {
		if a, err := netip.ParseAddr(s); err == nil && pfx.Contains(a) {
			return true
		}
	}
	return false
}

// splitDst separates "alias:ports". Without a trailing port spec the whole entry
// is the alias and every port is covered (grants carry ports elsewhere).
func splitDst(entry string) (alias, ports string) {
	i := strings.LastIndexByte(entry, ':')
	if i <= 0 {
		return entry, "*"
	}
	if portSpec(entry[i+1:]) {
		return entry[:i], entry[i+1:]
	}
	return entry, "*"
}

func portSpec(s string) bool {
	if s == "*" {
		return true
	}
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		if _, err := strconv.Atoi(lo); err != nil {
			return false
		}
		if isRange {
			if _, err := strconv.Atoi(hi); err != nil {
				return false
			}
		}
	}
	return true
}

func portsCover(spec string, port int) bool {
	if spec == "*" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		l, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		h := l
		if isRange {
			if h, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		if port >= l && port <= h {
			return true
		}
	}
	return false
}

// ipCovers handles a grants "ip" entry: "*", "7433", "tcp:7433", "tcp:*".
func ipCovers(entry string, port int) bool {
	if entry == "*" {
		return true
	}
	if proto, ports, ok := strings.Cut(entry, ":"); ok {
		if proto != "tcp" && proto != "6" {
			return false
		}
		return portsCover(ports, port)
	}
	if portSpec(entry) {
		return portsCover(entry, port)
	}
	return false
}
