package meshadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
	"tailscale.com/tailcfg"
)

// Custom DERP regions use ids 900 to 999 on the official control plane
// (https://tailscale.com/docs/reference/derp-servers/custom-derp-servers).
const (
	MinCustomRegionID = 900
	MaxCustomRegionID = 999
)

// DERPRegionSpec describes one self-hosted DERP server as a region with a
// single node, which is all the official derpMap supports.
type DERPRegionSpec struct {
	// ID is the region id; 0 means "allocate the lowest free id in 900-999".
	ID       int    `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	IPv4     string `json:"ipv4,omitempty"`
	IPv6     string `json:"ipv6,omitempty"`
	DERPPort int    `json:"derp_port,omitempty"` // default 443
	STUNPort int    `json:"stun_port,omitempty"` // default 3478
	// CertName is the TLS name when it differs from Hostname.
	CertName string `json:"cert_name,omitempty"`
}

var (
	hostnamePattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,}$`)
	regionCodeRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
)

// Validate checks one spec and returns warnings (not errors) for risky choices.
func (s DERPRegionSpec) Validate() (warnings []string, err error) {
	if !regionCodeRe.MatchString(s.Code) {
		return nil, fmt.Errorf("%w: region code %q (lowercase letters, digits, '-')", ErrInvalid, s.Code)
	}
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("%w: region %q needs a name", ErrInvalid, s.Code)
	}
	if !hostnamePattern.MatchString(s.Hostname) {
		return nil, fmt.Errorf("%w: DERP hostname %q must be a DNS name without scheme or port", ErrInvalid, s.Hostname)
	}
	if s.ID != 0 && (s.ID < MinCustomRegionID || s.ID > MaxCustomRegionID) {
		return nil, fmt.Errorf("%w: region id %d outside %d-%d", ErrInvalid, s.ID, MinCustomRegionID, MaxCustomRegionID)
	}
	if s.IPv4 != "" {
		if a, e := netip.ParseAddr(s.IPv4); e != nil || !a.Is4() {
			return nil, fmt.Errorf("%w: ipv4 %q", ErrInvalid, s.IPv4)
		}
	}
	if s.IPv6 != "" {
		if a, e := netip.ParseAddr(s.IPv6); e != nil || !a.Is6() || a.Is4In6() {
			return nil, fmt.Errorf("%w: ipv6 %q", ErrInvalid, s.IPv6)
		}
	}
	for _, p := range []int{s.DERPPort, s.STUNPort} {
		if p < 0 || p > 65535 {
			return nil, fmt.Errorf("%w: port %d", ErrInvalid, p)
		}
	}
	if s.IPv4 == "" && s.IPv6 == "" {
		warnings = append(warnings, fmt.Sprintf("region %s lists no IP address; clients fall back to DNS and lose the relay when DNS fails", s.Code))
	}
	return warnings, nil
}

func (s DERPRegionSpec) node(id int) *tailcfg.DERPNode {
	n := &tailcfg.DERPNode{
		Name: fmt.Sprintf("%d%s", id, "a"), RegionID: tailcfg.DERPRegionID(id), HostName: s.Hostname,
		IPv4: s.IPv4, IPv6: s.IPv6, CertName: s.CertName,
	}
	if s.DERPPort != 0 && s.DERPPort != 443 {
		n.DERPPort = s.DERPPort
	}
	if s.STUNPort != 0 && s.STUNPort != 3478 {
		n.STUNPort = s.STUNPort
	}
	return n
}

// ExistingRegionIDs lists the derpMap.regions ids present in a policy text
// (managed or not), for collision-free allocation.
func ExistingRegionIDs(policyText string) (map[int]bool, error) {
	ids := map[int]bool{}
	if strings.TrimSpace(policyText) == "" {
		return ids, nil
	}
	std, err := StandardJSON(policyText)
	if err != nil {
		return nil, err
	}
	var p struct {
		DERPMap struct {
			Regions map[string]json.RawMessage `json:"regions"`
		} `json:"derpMap"`
	}
	if err := json.Unmarshal(std, &p); err != nil {
		return nil, fmt.Errorf("%w: derpMap shape: %v", ErrInvalid, err)
	}
	for k := range p.DERPMap.Regions {
		var n int
		if _, err := fmt.Sscanf(k, "%d", &n); err == nil {
			ids[n] = true
		}
	}
	return ids, nil
}

// AssignRegionIDs gives every spec without an id the lowest free one in
// 900-999, never reusing an id in taken or one used by another spec. The input
// slice is not modified.
func AssignRegionIDs(specs []DERPRegionSpec, taken map[int]bool) ([]DERPRegionSpec, error) {
	used := map[int]bool{}
	for id := range taken {
		used[id] = true
	}
	out := append([]DERPRegionSpec(nil), specs...)
	for _, s := range out {
		if s.ID != 0 {
			if used[s.ID] && !taken[s.ID] {
				return nil, fmt.Errorf("%w: region id %d used twice", ErrInvalid, s.ID)
			}
			used[s.ID] = true
		}
	}
	next := MinCustomRegionID
	for i := range out {
		if out[i].ID != 0 {
			continue
		}
		for used[next] && next <= MaxCustomRegionID {
			next++
		}
		if next > MaxCustomRegionID {
			return nil, fmt.Errorf("%w: no free region id left in %d-%d", ErrInvalid, MinCustomRegionID, MaxCustomRegionID)
		}
		out[i].ID = next
		used[next] = true
	}
	return out, nil
}

// DERPOptions control a DERP write.
type DERPOptions struct {
	// OmitDefaultRegions drops the official regions so only the listed ones are used.
	OmitDefaultRegions bool
}

// officialRegion mirrors the JSON the official policy accepts under derpMap.regions.
type officialRegion struct {
	RegionID   int            `json:"regionID"`
	RegionCode string         `json:"regionCode"`
	RegionName string         `json:"regionName"`
	Nodes      []officialNode `json:"nodes"`
}

type officialNode struct {
	Name     string `json:"name"`
	RegionID int    `json:"regionID"`
	HostName string `json:"hostName"`
	CertName string `json:"certName,omitempty"`
	IPv4     string `json:"ipv4,omitempty"`
	IPv6     string `json:"ipv6,omitempty"`
	STUNPort int    `json:"stunPort,omitempty"`
	DERPPort int    `json:"derpPort,omitempty"`
}

// DERPFragment turns specs (ids already assigned) into the policy fragment for
// the official control plane, plus warnings.
func DERPFragment(specs []DERPRegionSpec, opt DERPOptions) (Fragment, []string, error) {
	var warnings []string
	regions := map[int]json.RawMessage{}
	for _, s := range specs {
		if s.ID == 0 {
			return Fragment{}, nil, fmt.Errorf("%w: region %q has no id (call AssignRegionIDs)", ErrInvalid, s.Code)
		}
		w, err := s.Validate()
		if err != nil {
			return Fragment{}, nil, err
		}
		warnings = append(warnings, w...)
		n := s.node(s.ID)
		b, _ := json.Marshal(officialRegion{
			RegionID: s.ID, RegionCode: s.Code, RegionName: s.Name,
			Nodes: []officialNode{{Name: n.Name, RegionID: s.ID, HostName: n.HostName, CertName: n.CertName, IPv4: n.IPv4, IPv6: n.IPv6, STUNPort: n.STUNPort, DERPPort: n.DERPPort}},
		})
		regions[s.ID] = b
	}
	if len(specs) == 1 && opt.OmitDefaultRegions {
		warnings = append(warnings, "only one DERP region and the official regions are omitted: that server is a single point of failure")
	}
	omit := opt.OmitDefaultRegions
	return Fragment{DERPRegions: regions, OmitDefaultRegions: &omit}, warnings, nil
}

// HeadscaleDERP is the server-side configuration Headscale needs: a DERP map
// file for derp.paths and the config.yaml lines that reference it.
type HeadscaleDERP struct {
	File    []byte `json:"file"`
	Snippet string `json:"snippet"`
}

// HeadscaleDERPConfig renders specs as the YAML file Headscale loads from
// derp.paths and the matching configuration snippet. path is where the file
// will live on the Headscale host. Headscale reads derp.* at startup (and on
// its periodic refresh for urls), so the operator restarts or reloads it.
func HeadscaleDERPConfig(specs []DERPRegionSpec, opt DERPOptions, path string) (HeadscaleDERP, []string, error) {
	var warnings []string
	dm := &tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{}}
	for _, s := range specs {
		if s.ID == 0 {
			return HeadscaleDERP{}, nil, fmt.Errorf("%w: region %q has no id (call AssignRegionIDs)", ErrInvalid, s.Code)
		}
		w, err := s.Validate()
		if err != nil {
			return HeadscaleDERP{}, nil, err
		}
		warnings = append(warnings, w...)
		dm.Regions[tailcfg.DERPRegionID(s.ID)] = &tailcfg.DERPRegion{
			RegionID: tailcfg.DERPRegionID(s.ID), RegionCode: s.Code, RegionName: s.Name,
			Nodes: []*tailcfg.DERPNode{s.node(s.ID)},
		}
	}
	if len(specs) == 1 && opt.OmitDefaultRegions {
		warnings = append(warnings, "only one DERP region and the official regions are omitted: that server is a single point of failure")
	}
	// Headscale decodes the file with a YAML library without struct tags, so
	// field names are the lowercased Go names; marshalling the same type with the
	// same library produces exactly that shape.
	body, err := yaml.Marshal(dm)
	if err != nil {
		return HeadscaleDERP{}, nil, err
	}
	var sn strings.Builder
	sn.WriteString("derp:\n")
	if opt.OmitDefaultRegions {
		sn.WriteString("  urls: []   # drop the official DERP map\n")
	} else {
		sn.WriteString("  # keep derp.urls as it is to stay on the official regions as well\n")
	}
	fmt.Fprintf(&sn, "  paths:\n    - %s\n", path)
	return HeadscaleDERP{File: body, Snippet: sn.String()}, warnings, nil
}

// ApplyDERPResult reports a DERP write and its probes.
type ApplyDERPResult struct {
	ApplyResult
	Probes     []ProbeResult `json:"probes"`
	RolledBack bool          `json:"rolled_back"`
}

// ErrProbeFailed means a freshly written DERP region failed its health probe.
var ErrProbeFailed = fmt.Errorf("meshadmin: DERP probe failed")

// ApplyDERP writes the regions into the official policy, probes every region
// and restores the previous policy when a probe fails (snapshot first, then
// rollback), as the design requires. probe may be nil to skip probing.
func ApplyDERP(ctx context.Context, p Provider, store SnapshotStore, plan *PolicyPlan, specs []DERPRegionSpec, probe func(context.Context, DERPRegionSpec) ProbeResult) (ApplyDERPResult, error) {
	res, err := plan.Apply(ctx, p, store, true)
	out := ApplyDERPResult{ApplyResult: res}
	if err != nil {
		return out, err
	}
	if probe == nil {
		return out, nil
	}
	failed := false
	for _, s := range specs {
		r := probe(ctx, s)
		out.Probes = append(out.Probes, r)
		failed = failed || !r.OK
	}
	sort.Slice(out.Probes, func(i, j int) bool { return out.Probes[i].Host < out.Probes[j].Host })
	if failed {
		if res.SnapshotID == "" {
			return out, fmt.Errorf("%w; no earlier policy existed to restore", ErrProbeFailed)
		}
		if _, rerr := Rollback(ctx, p, store, res.SnapshotID); rerr != nil {
			return out, fmt.Errorf("%w; automatic rollback failed: %v (snapshot %s)", ErrProbeFailed, rerr, res.SnapshotID)
		}
		out.RolledBack = true
		return out, fmt.Errorf("%w; previous policy restored from snapshot %s", ErrProbeFailed, res.SnapshotID)
	}
	return out, nil
}
