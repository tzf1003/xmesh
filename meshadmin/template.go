package meshadmin

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// xmesh tags and the role each one grants. The list is the contract in
// docs/contracts/meshadmin.md; the kernel reads the same names from
// agent.authz and WhoIs.
const (
	TagDevice        = "tag:device"         // Agent: discoverable, accepts tool calls
	TagPeer          = "tag:peer"           // trusted caller (a Gateway on a user device)
	TagIngress       = "tag:ingress"        // public entry node
	TagAdmin         = "tag:admin"          // may drain nodes
	TagIngressTarget = "tag:ingress-target" // devices a restricted ingress may reach
)

// AgentPort is the TCP port every Agent listens on inside the tailnet.
const AgentPort = 7433

// TemplateName selects a permission template.
type TemplateName string

const (
	TemplatePersonal   TemplateName = "personal"
	TemplateTeam       TemplateName = "team"
	TemplateRestricted TemplateName = "restricted"
)

// TemplateNames lists the templates in documentation order.
func TemplateNames() []TemplateName {
	return []TemplateName{TemplatePersonal, TemplateTeam, TemplateRestricted}
}

// TemplateOptions parameterise a template.
type TemplateOptions struct {
	// Owner becomes the tagOwners value for every xmesh tag, for example
	// "autogroup:admin" (Tailscale) or "alice@" (Headscale user). Required.
	Owner string
	// Port overrides AgentPort.
	Port int
}

var ownerPattern = regexp.MustCompile(`^(autogroup:[a-z]+|group:[A-Za-z0-9._-]+|tag:[A-Za-z0-9-]+|[^\s@]*@[^\s]*)$`)

// BuildTemplate returns the managed fragment for a template. Rules are the
// network-layer half of the double authorisation (CON-SEC-01); agent.authz
// still decides which tools each tag may call.
//
//	personal    device to device, peer/ingress/admin to device
//	team        only peer/ingress/admin may open connections; devices cannot
//	restricted  like team, but the ingress reaches only tag:ingress-target devices
func BuildTemplate(name TemplateName, opt TemplateOptions) (Fragment, error) {
	if !ownerPattern.MatchString(opt.Owner) {
		return Fragment{}, fmt.Errorf("%w: tag owner %q (use autogroup:admin, group:x, tag:x or user@)", ErrInvalid, opt.Owner)
	}
	port := opt.Port
	if port == 0 {
		port = AgentPort
	}
	if port < 1 || port > 65535 {
		return Fragment{}, fmt.Errorf("%w: port %d", ErrInvalid, port)
	}
	dst := TagDevice + ":" + strconv.Itoa(port)
	tags := []string{TagDevice, TagPeer, TagIngress, TagAdmin}
	var rules []ACLRule
	switch name {
	case TemplatePersonal:
		rules = []ACLRule{
			{Src: []string{TagDevice, TagPeer, TagIngress, TagAdmin}, Dst: []string{dst}},
		}
	case TemplateTeam:
		rules = []ACLRule{
			{Src: []string{TagPeer, TagIngress, TagAdmin}, Dst: []string{dst}},
		}
	case TemplateRestricted:
		tags = append(tags, TagIngressTarget)
		rules = []ACLRule{
			{Src: []string{TagPeer, TagAdmin}, Dst: []string{dst}},
			{Src: []string{TagIngress}, Dst: []string{TagIngressTarget + ":" + strconv.Itoa(port)}},
		}
	default:
		return Fragment{}, fmt.Errorf("%w: unknown template %q (want personal, team or restricted)", ErrInvalid, name)
	}
	owners := map[string][]string{}
	for _, t := range tags {
		owners[t] = []string{opt.Owner}
	}
	for i := range rules {
		rules[i].Action = "accept"
		sort.Strings(rules[i].Src)
	}
	return Fragment{TagOwners: owners, ACLs: rules}, nil
}

var tagPattern = regexp.MustCompile(`^tag:[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

// ValidTag reports whether s is a legal control-plane tag name.
func ValidTag(s string) bool { return tagPattern.MatchString(s) }

// CanonicalTags validates, de-duplicates and sorts tags.
func CanonicalTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if !ValidTag(t) {
			return nil, fmt.Errorf("%w: %q is not a valid tag (want tag:name)", ErrInvalid, t)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}
