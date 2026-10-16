package meshadmin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// ManagedMarker tags every policy element xmesh writes. It is a comment placed
// directly before the element, so the policy stays valid HuJSON on both
// control planes and users can see what xmesh owns. Elements without it are
// never modified or removed (RUL-21).
const ManagedMarker = "xmesh:managed"

// Section names the policy parts xmesh may manage.
type Section string

const (
	SectionTagOwners Section = "tagOwners"
	SectionACLs      Section = "acls"
	SectionDERP      Section = "derpMap"
)

// ACLRule is the subset of a policy accept rule that xmesh templates emit.
type ACLRule struct {
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

// Fragment is the managed content for one apply. A nil/empty field removes the
// managed elements of that section.
type Fragment struct {
	TagOwners map[string][]string
	ACLs      []ACLRule
	// DERPRegions maps region id to the region object (already JSON-shaped).
	DERPRegions map[int]json.RawMessage
	// OmitDefaultRegions is written into derpMap when DERPRegions is non-empty and
	// the field is set; the pointer distinguishes "leave alone" from "false".
	OmitDefaultRegions *bool
}

// ParsePolicy parses HuJSON policy text; the root must be an object.
func ParsePolicy(text string) (hujson.Value, error) {
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	v, err := hujson.Parse([]byte(text))
	if err != nil {
		return hujson.Value{}, fmt.Errorf("%w: policy is not valid HuJSON: %v", ErrInvalid, err)
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return hujson.Value{}, fmt.Errorf("%w: policy root must be an object", ErrInvalid)
	}
	return v, nil
}

// ApplyFragment replaces the managed elements of the given sections and returns
// the new text plus warnings. Every byte outside managed elements is preserved,
// including comments, whitespace and key order.
func ApplyFragment(text string, f Fragment, sections ...Section) (string, []string, error) {
	v, err := ParsePolicy(text)
	if err != nil {
		return "", nil, err
	}
	root := v.Value.(*hujson.Object)
	var warnings []string
	for _, s := range sections {
		switch s {
		case SectionTagOwners:
			warnings = append(warnings, replaceTagOwners(root, f.TagOwners)...)
		case SectionACLs:
			replaceACLs(root, f.ACLs)
		case SectionDERP:
			if err := replaceDERP(root, f); err != nil {
				return "", nil, err
			}
		default:
			return "", nil, fmt.Errorf("%w: unknown section %q", ErrInvalid, s)
		}
	}
	out := string(v.Pack())
	if _, err := hujson.Parse([]byte(out)); err != nil {
		return "", nil, fmt.Errorf("meshadmin: internal error: edited policy does not parse: %w", err)
	}
	return out, warnings, nil
}

// ManagedSections reports which sections currently contain xmesh-managed elements.
func ManagedSections(text string) ([]Section, error) {
	v, err := ParsePolicy(text)
	if err != nil {
		return nil, err
	}
	root := v.Value.(*hujson.Object)
	var out []Section
	if m := member(root, "tagOwners"); m != nil {
		if obj, ok := m.Value.Value.(*hujson.Object); ok && anyManagedMember(obj) {
			out = append(out, SectionTagOwners)
		}
	}
	if m := member(root, "acls"); m != nil {
		if arr, ok := m.Value.Value.(*hujson.Array); ok && anyManagedElement(arr) {
			out = append(out, SectionACLs)
		}
	}
	if m := member(root, "derpMap"); m != nil {
		if dm, ok := m.Value.Value.(*hujson.Object); ok {
			if r := member(dm, "regions"); r != nil {
				if obj, ok := r.Value.Value.(*hujson.Object); ok && anyManagedMember(obj) {
					out = append(out, SectionDERP)
				}
			}
		}
	}
	return out, nil
}

func isManaged(e hujson.Extra) bool { return bytes.Contains(e, []byte(ManagedMarker)) }

func anyManagedMember(o *hujson.Object) bool {
	for _, m := range o.Members {
		if isManaged(m.Name.BeforeExtra) {
			return true
		}
	}
	return false
}

func anyManagedElement(a *hujson.Array) bool {
	for _, e := range a.Elements {
		if isManaged(e.BeforeExtra) {
			return true
		}
	}
	return false
}

func memberIndex(o *hujson.Object, name string) int {
	for i, m := range o.Members {
		if lit, ok := m.Name.Value.(hujson.Literal); ok && lit.String() == name {
			return i
		}
	}
	return -1
}

func member(o *hujson.Object, name string) *hujson.ObjectMember {
	if i := memberIndex(o, name); i >= 0 {
		return &o.Members[i]
	}
	return nil
}

// lineIndent returns the whitespace after the last newline in extra, or "" when
// extra has no newline.
func lineIndent(e hujson.Extra) string {
	i := bytes.LastIndexByte(e, '\n')
	if i < 0 {
		return ""
	}
	rest := e[i+1:]
	n := 0
	for n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
		n++
	}
	return string(rest[:n])
}

func memberIndent(o *hujson.Object, fallback string) string {
	for _, m := range o.Members {
		if bytes.IndexByte(m.Name.BeforeExtra, '\n') >= 0 {
			return lineIndent(m.Name.BeforeExtra)
		}
	}
	return fallback
}

func elementIndent(a *hujson.Array, fallback string) string {
	for _, e := range a.Elements {
		if bytes.IndexByte(e.BeforeExtra, '\n') >= 0 {
			return lineIndent(e.BeforeExtra)
		}
	}
	return fallback
}

func jsonLine(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func markerComment(indent, label string) hujson.Extra {
	return hujson.Extra("\n" + indent + "// " + ManagedMarker + " " + label + "\n" + indent)
}

// splitHead splits e at its first newline. The head is what sits on the same
// line as the preceding comma (usually a trailing line comment); the tail starts
// at the newline.
func splitHead(e hujson.Extra) (head, tail hujson.Extra) {
	i := bytes.IndexByte(e, '\n')
	if i < 0 {
		return e, nil
	}
	return e[:i], e[i:]
}

func hasComment(e hujson.Extra) bool {
	return bytes.Contains(e, []byte("//")) || bytes.Contains(e, []byte("/*"))
}

// A trailing comment after the last element's comma is parsed as part of the
// container's AfterExtra. adoptHead detaches it so a new element can be placed
// in front of it, and returns it as a prefix for the new element.
func adoptHead(containerAfter *hujson.Extra, n int) hujson.Extra {
	if n == 0 || !hasComment(*containerAfter) {
		return nil
	}
	head, tail := splitHead(*containerAfter)
	if !hasComment(head) {
		return nil
	}
	*containerAfter = tail
	return head
}

func prefix(head, e hujson.Extra) hujson.Extra {
	if len(head) == 0 {
		return e
	}
	return append(append(hujson.Extra{}, head...), e...)
}

// removeManagedElements deletes marked elements and repairs trailing-comma style.
func removeManagedElements(a *hujson.Array) {
	kept := a.Elements[:0:0]
	var pending hujson.Extra
	removed := false
	for i := range a.Elements {
		e := a.Elements[i]
		if isManaged(e.BeforeExtra) {
			removed = true
			if head, _ := splitHead(e.BeforeExtra); hasComment(head) {
				pending = prefix(pending, head)
			}
			continue
		}
		if len(pending) > 0 {
			e.BeforeExtra = prefix(pending, e.BeforeExtra)
			pending = nil
		}
		kept = append(kept, e)
	}
	if !removed {
		return
	}
	trailing := a.Elements[len(a.Elements)-1].AfterExtra != nil
	a.Elements = kept
	if len(pending) > 0 {
		a.AfterExtra = prefix(pending, a.AfterExtra)
	}
	if n := len(kept); n > 0 {
		fixTrailing(&kept[n-1].AfterExtra, trailing)
	}
}

func removeManagedMembers(o *hujson.Object) {
	kept := o.Members[:0:0]
	var pending hujson.Extra
	removed := false
	for _, m := range o.Members {
		if isManaged(m.Name.BeforeExtra) {
			removed = true
			if head, _ := splitHead(m.Name.BeforeExtra); hasComment(head) {
				pending = prefix(pending, head)
			}
			continue
		}
		if len(pending) > 0 {
			m.Name.BeforeExtra = prefix(pending, m.Name.BeforeExtra)
			pending = nil
		}
		kept = append(kept, m)
	}
	if !removed {
		return
	}
	trailing := o.Members[len(o.Members)-1].Value.AfterExtra != nil
	o.Members = kept
	if len(pending) > 0 {
		o.AfterExtra = prefix(pending, o.AfterExtra)
	}
	if n := len(kept); n > 0 {
		fixTrailing(&kept[n-1].Value.AfterExtra, trailing)
	}
}

// fixTrailing makes the last element's AfterExtra reflect the file's
// trailing-comma habit without discarding comments.
func fixTrailing(e *hujson.Extra, trailing bool) {
	if trailing && *e == nil {
		*e = hujson.Extra{}
	}
	if !trailing && len(*e) == 0 {
		*e = nil
	}
}

func appendElement(a *hujson.Array, v hujson.Value, outerIndent string) {
	trailing := false
	if n := len(a.Elements); n > 0 {
		trailing = a.Elements[n-1].AfterExtra != nil
		v.BeforeExtra = prefix(adoptHead(&a.AfterExtra, n), v.BeforeExtra)
	} else {
		a.AfterExtra = hujson.Extra("\n" + outerIndent)
		trailing = true
	}
	if trailing {
		v.AfterExtra = hujson.Extra{}
	}
	a.Elements = append(a.Elements, v)
}

func appendMember(o *hujson.Object, m hujson.ObjectMember, outerIndent string) {
	trailing := false
	if n := len(o.Members); n > 0 {
		trailing = o.Members[n-1].Value.AfterExtra != nil
		m.Name.BeforeExtra = prefix(adoptHead(&o.AfterExtra, n), m.Name.BeforeExtra)
	} else {
		o.AfterExtra = hujson.Extra("\n" + outerIndent)
		trailing = true
	}
	if trailing {
		m.Value.AfterExtra = hujson.Extra{}
	}
	o.Members = append(o.Members, m)
}

func parseTrimmed(raw []byte) hujson.Value {
	v, err := hujson.Parse(raw)
	if err != nil {
		panic("meshadmin: generated JSON does not parse: " + err.Error())
	}
	return v
}

// topLevel returns the object member for name, creating it with an empty value
// of the given kind when absent.
func topLevel(root *hujson.Object, name string, empty string) (*hujson.ObjectMember, string) {
	ind := memberIndent(root, "\t")
	if m := member(root, name); m != nil {
		return m, ind
	}
	nm := hujson.ObjectMember{
		Name:  hujson.Value{BeforeExtra: hujson.Extra("\n" + ind), Value: hujson.String(name)},
		Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: parseTrimmed([]byte(empty)).Value},
	}
	if len(root.Members) == 0 {
		root.AfterExtra = hujson.Extra("\n")
	}
	appendMember(root, nm, "")
	return &root.Members[len(root.Members)-1], ind
}

func replaceACLs(root *hujson.Object, rules []ACLRule) {
	m := member(root, "acls")
	if m == nil && len(rules) == 0 {
		return
	}
	if m != nil {
		if arr, ok := m.Value.Value.(*hujson.Array); ok {
			removeManagedElements(arr)
		}
	}
	if len(rules) == 0 {
		return
	}
	m, ind := topLevel(root, "acls", "[]")
	arr, ok := m.Value.Value.(*hujson.Array)
	if !ok {
		return
	}
	inner := elementIndent(arr, ind+"\t")
	for _, r := range rules {
		if r.Action == "" {
			r.Action = "accept"
		}
		v := parseTrimmed(jsonLine(r))
		v.BeforeExtra = markerComment(inner, "acl")
		appendElement(arr, v, ind)
	}
}

func replaceTagOwners(root *hujson.Object, owners map[string][]string) []string {
	m := member(root, "tagOwners")
	if m == nil && len(owners) == 0 {
		return nil
	}
	if m != nil {
		if obj, ok := m.Value.Value.(*hujson.Object); ok {
			removeManagedMembers(obj)
		}
	}
	if len(owners) == 0 {
		return nil
	}
	m, ind := topLevel(root, "tagOwners", "{}")
	obj, ok := m.Value.Value.(*hujson.Object)
	if !ok {
		return []string{"tagOwners is not an object; left untouched"}
	}
	var warnings []string
	inner := memberIndent(obj, ind+"\t")
	names := make([]string, 0, len(owners))
	for n := range owners {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if memberIndex(obj, n) >= 0 {
			warnings = append(warnings, fmt.Sprintf("tagOwners already defines %s outside xmesh; kept as is", n))
			continue
		}
		nm := hujson.ObjectMember{
			Name:  hujson.Value{BeforeExtra: markerComment(inner, "tagOwners"), Value: hujson.String(n)},
			Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: parseTrimmed(jsonLine(owners[n])).Value},
		}
		appendMember(obj, nm, ind)
	}
	return warnings
}

func replaceDERP(root *hujson.Object, f Fragment) error {
	dm := member(root, "derpMap")
	if dm == nil && len(f.DERPRegions) == 0 {
		return nil
	}
	if dm != nil {
		if obj, ok := dm.Value.Value.(*hujson.Object); ok {
			removeManagedMembers(obj) // omitDefaultRegions, when xmesh wrote it
			if r := member(obj, "regions"); r != nil {
				if ro, ok := r.Value.Value.(*hujson.Object); ok {
					removeManagedMembers(ro)
				}
			}
		}
	}
	if len(f.DERPRegions) == 0 {
		return nil
	}
	dm, ind := topLevel(root, "derpMap", "{}")
	obj, ok := dm.Value.Value.(*hujson.Object)
	if !ok {
		return fmt.Errorf("%w: derpMap is not an object", ErrInvalid)
	}
	inner := memberIndent(obj, ind+"\t")
	if f.OmitDefaultRegions != nil && member(obj, "omitDefaultRegions") == nil {
		appendMember(obj, hujson.ObjectMember{
			Name:  hujson.Value{BeforeExtra: markerComment(inner, "derpMap"), Value: hujson.String("omitDefaultRegions")},
			Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: hujson.Bool(*f.OmitDefaultRegions)},
		}, ind)
	}
	rm := member(obj, "regions")
	if rm == nil {
		appendMember(obj, hujson.ObjectMember{
			Name:  hujson.Value{BeforeExtra: hujson.Extra("\n" + inner), Value: hujson.String("regions")},
			Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: parseTrimmed([]byte("{}")).Value},
		}, ind)
		rm = &obj.Members[len(obj.Members)-1]
	}
	robj, ok := rm.Value.Value.(*hujson.Object)
	if !ok {
		return fmt.Errorf("%w: derpMap.regions is not an object", ErrInvalid)
	}
	regionInd := memberIndent(robj, inner+"\t")
	ids := make([]int, 0, len(f.DERPRegions))
	for id := range f.DERPRegions {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		key := strconv.Itoa(id)
		if memberIndex(robj, key) >= 0 {
			return fmt.Errorf("%w: derpMap region %s already exists and is not managed by xmesh", ErrInvalid, key)
		}
		appendMember(robj, hujson.ObjectMember{
			Name:  hujson.Value{BeforeExtra: markerComment(regionInd, "derpMap"), Value: hujson.String(key)},
			Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: parseTrimmed(f.DERPRegions[id]).Value},
		}, inner)
	}
	return nil
}

// StandardJSON returns the policy as plain JSON (comments stripped) for analysis.
func StandardJSON(text string) ([]byte, error) {
	b, err := hujson.Standardize([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("%w: policy is not valid HuJSON: %v", ErrInvalid, err)
	}
	return b, nil
}
