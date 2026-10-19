package meshadmin

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// DocumentEdit changes a local policy document. It never accesses a control plane.
// Explicit region edits may update user-created regions; automatic templates
// retain the managed-element ownership rules of ApplyFragment.
type DocumentEdit struct {
	Text        string              `json:"text"`
	Template    *TemplateName       `json:"template,omitempty"`
	Owner       string              `json:"owner,omitempty"`
	Port        int                 `json:"port,omitempty"`
	Regions     []DERPRegionSpec    `json:"regions,omitempty"`
	Region      *DocumentRegionEdit `json:"region,omitempty"`
	RemoveID    *int                `json:"removeID,omitempty"`
	OmitDefault *bool               `json:"omitDefault,omitempty"`
}

type DocumentRegionEdit struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type DocumentRegion struct {
	ID    int      `json:"id"`
	Code  string   `json:"code"`
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
	Text  string   `json:"text"`
}

type PolicyDocument struct {
	Text        string           `json:"text"`
	Regions     []DocumentRegion `json:"regions"`
	OmitDefault bool             `json:"omitDefault"`
}

// documentMember accepts the Go-shaped names used by existing DERP maps as
// well as the lower-camel-case names, preserving the spelling in the document.
func documentMember(o *hujson.Object, name string) *hujson.ObjectMember {
	for i := range o.Members {
		if strings.EqualFold(o.Members[i].Name.Value.(hujson.Literal).String(), name) {
			return &o.Members[i]
		}
	}
	return nil
}

func documentObject(o *hujson.Object, name string) (*hujson.Object, error) {
	m := documentMember(o, name)
	if m == nil {
		m, _ = topLevel(o, name, "{}")
	}
	v, ok := m.Value.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be an object", ErrInvalid, name)
	}
	return v, nil
}

func putDocumentMember(o *hujson.Object, name string, v hujson.Value) {
	if m := documentMember(o, name); m != nil {
		// Keep surrounding comments, separators, and all unrelated bytes.
		m.Value.Value = v.Value
		return
	}
	appendMember(o, hujson.ObjectMember{Name: hujson.Value{BeforeExtra: hujson.Extra("\n\t"), Value: hujson.String(name)}, Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: v.Value}}, "")
}

func InspectPolicyDocument(text string) (PolicyDocument, error) {
	if strings.TrimSpace(text) == "" {
		return PolicyDocument{}, fmt.Errorf("%w: policy document is empty", ErrInvalid)
	}
	v, err := ParsePolicy(text)
	if err != nil {
		return PolicyDocument{}, err
	}
	out := PolicyDocument{Text: text, Regions: []DocumentRegion{}}
	dm := documentMember(v.Value.(*hujson.Object), "derpMap")
	if dm == nil {
		return out, nil
	}
	obj, ok := dm.Value.Value.(*hujson.Object)
	if !ok {
		return out, fmt.Errorf("%w: derpMap must be an object", ErrInvalid)
	}
	if m := documentMember(obj, "omitDefaultRegions"); m != nil {
		standard, err := hujson.Standardize(m.Value.Pack())
		if err != nil {
			return out, fmt.Errorf("%w: invalid omitDefaultRegions", ErrInvalid)
		}
		if err := json.Unmarshal(standard, &out.OmitDefault); err != nil {
			return out, fmt.Errorf("%w: omitDefaultRegions must be boolean", ErrInvalid)
		}
	}
	r := documentMember(obj, "regions")
	if r == nil {
		return out, nil
	}
	regions, ok := r.Value.Value.(*hujson.Object)
	if !ok {
		return out, fmt.Errorf("%w: derpMap.regions must be an object", ErrInvalid)
	}
	for _, m := range regions.Members {
		id, err := strconv.Atoi(m.Name.Value.(hujson.Literal).String())
		if err != nil || id <= 0 {
			return out, fmt.Errorf("%w: invalid DERP region id", ErrInvalid)
		}
		// Parse packed bytes instead of Clone: preserving non-nil empty trivia
		// matters because HuJSON uses it to represent trailing commas.
		raw, err := hujson.Parse(m.Value.Pack())
		if err != nil {
			return out, fmt.Errorf("%w: invalid DERP region", ErrInvalid)
		}
		raw.BeforeExtra, raw.AfterExtra = nil, nil
		std, err := hujson.Standardize(raw.Pack())
		if err != nil {
			return out, fmt.Errorf("%w: invalid DERP region", ErrInvalid)
		}
		var region struct {
			RegionID               int
			RegionCode, RegionName string
			Nodes                  []struct {
				HostName, IPv4, IPv6 string
				RegionID             int
			}
		}
		if err := json.Unmarshal(std, &region); err != nil || region.RegionID != id || region.RegionCode == "" || region.RegionName == "" || len(region.Nodes) == 0 {
			return out, fmt.Errorf("%w: malformed DERP region %d", ErrInvalid, id)
		}
		hosts := []string{}
		for _, n := range region.Nodes {
			if (n.HostName == "" && n.IPv4 == "" && n.IPv6 == "") || n.RegionID != id {
				return out, fmt.Errorf("%w: malformed DERP node in region %d", ErrInvalid, id)
			}
			address := n.HostName
			if address == "" {
				address = strings.TrimSpace(n.IPv4 + " " + n.IPv6)
			}
			hosts = append(hosts, address)
		}
		out.Regions = append(out.Regions, DocumentRegion{ID: id, Code: region.RegionCode, Name: region.RegionName, Hosts: hosts, Text: string(raw.Pack())})
	}
	sort.Slice(out.Regions, func(i, j int) bool { return out.Regions[i].ID < out.Regions[j].ID })
	return out, nil
}

func EditPolicyDocument(edit DocumentEdit) (PolicyDocument, error) {
	if strings.TrimSpace(edit.Text) == "" {
		return PolicyDocument{}, fmt.Errorf("%w: policy document is empty", ErrInvalid)
	}
	text := edit.Text
	if edit.Template != nil {
		frag, err := BuildTemplate(*edit.Template, TemplateOptions{Owner: edit.Owner, Port: edit.Port})
		if err != nil {
			return PolicyDocument{}, err
		}
		text, _, err = ApplyFragment(text, frag, SectionTagOwners, SectionACLs)
		if err != nil {
			return PolicyDocument{}, err
		}
	}
	v, err := ParsePolicy(text)
	if err != nil {
		return PolicyDocument{}, err
	}
	root := v.Value.(*hujson.Object)
	if edit.Region != nil || edit.RemoveID != nil || edit.OmitDefault != nil || len(edit.Regions) > 0 {
		dm, err := documentObject(root, "derpMap")
		if err != nil {
			return PolicyDocument{}, err
		}
		if edit.OmitDefault != nil {
			putDocumentMember(dm, "omitDefaultRegions", hujson.Value{Value: hujson.Bool(*edit.OmitDefault)})
		}
		regions, err := documentObject(dm, "regions")
		if err != nil {
			return PolicyDocument{}, err
		}
		if edit.RemoveID != nil {
			i := memberIndex(regions, strconv.Itoa(*edit.RemoveID))
			if i < 0 {
				return PolicyDocument{}, fmt.Errorf("%w: DERP region not found", ErrInvalid)
			}
			trailing := regions.Members[len(regions.Members)-1].Value.AfterExtra != nil
			regions.Members = append(regions.Members[:i], regions.Members[i+1:]...)
			if n := len(regions.Members); n > 0 {
				fixTrailing(&regions.Members[n-1].Value.AfterExtra, trailing)
			}
		}
		if edit.Region != nil {
			if memberIndex(regions, strconv.Itoa(edit.Region.ID)) < 0 {
				return PolicyDocument{}, fmt.Errorf("%w: DERP region not found", ErrInvalid)
			}
			r, err := hujson.Parse([]byte(edit.Region.Text))
			if err != nil {
				return PolicyDocument{}, fmt.Errorf("%w: invalid DERP region", ErrInvalid)
			}
			putDocumentMember(regions, strconv.Itoa(edit.Region.ID), r)
		}
		for _, s := range edit.Regions {
			if s.ID == 0 {
				for id := MinCustomRegionID; id <= MaxCustomRegionID; id++ {
					if memberIndex(regions, strconv.Itoa(id)) < 0 {
						s.ID = id
						break
					}
				}
			}
			if memberIndex(regions, strconv.Itoa(s.ID)) >= 0 {
				return PolicyDocument{}, fmt.Errorf("%w: DERP region already exists", ErrInvalid)
			}
			frag, _, err := DERPFragment([]DERPRegionSpec{s}, DERPOptions{})
			if err != nil {
				return PolicyDocument{}, err
			}
			putDocumentMember(regions, strconv.Itoa(s.ID), parseTrimmed(frag.DERPRegions[s.ID]))
		}
	}
	return InspectPolicyDocument(string(v.Pack()))
}

// ChangedDocumentRelays selects only changed connection parameters. Renaming a
// region or editing unrelated policy fields does not trigger network probes.
func ChangedDocumentRelays(before, after string) ([]DERPRegionSpec, error) {
	type node struct {
		Name                           string
		HostName, IPv4, IPv6, CertName string
		DERPPort, STUNPort             int
		STUNOnly                       bool
	}
	read := func(text string) (map[string]node, error) {
		if strings.TrimSpace(text) == "" {
			return map[string]node{}, nil
		}
		std, err := StandardJSON(text)
		if err != nil {
			return nil, err
		}
		var value struct {
			DERPMap struct {
				Regions map[string]struct{ Nodes []node }
			}
		}
		if err := json.Unmarshal(std, &value); err != nil {
			return nil, fmt.Errorf("%w: invalid DERP map", ErrInvalid)
		}
		out := map[string]node{}
		for id, region := range value.DERPMap.Regions {
			for _, n := range region.Nodes {
				if n.DERPPort == 0 {
					n.DERPPort = 443
				}
				if n.STUNPort == 0 {
					n.STUNPort = 3478
				}
				out[id+"/"+n.Name] = n
			}
		}
		return out, nil
	}
	old, err := read(before)
	if err != nil {
		return nil, err
	}
	next, err := read(after)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(next))
	for key := range next {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []DERPRegionSpec
	for _, key := range keys {
		n := next[key]
		if prior, ok := old[key]; ok && reflect.DeepEqual(prior, n) {
			continue
		}
		if n.STUNOnly || n.STUNPort < 0 {
			return nil, fmt.Errorf("%w: changed STUN-only or disabled-STUN nodes require a dedicated probe", ErrUnsupported)
		}
		out = append(out, DERPRegionSpec{Hostname: n.HostName, IPv4: n.IPv4, IPv6: n.IPv6, DERPPort: n.DERPPort, STUNPort: n.STUNPort, CertName: n.CertName})
	}
	return out, nil
}
