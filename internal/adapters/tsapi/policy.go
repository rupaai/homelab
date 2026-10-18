package tsapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/tailscale/hujson"
)

var (
	groupNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	tagRe       = regexp.MustCompile(`^tag:[a-z0-9-]+$`)
)

// ManagerTag is the tag on Homelab's OAuth client. It owns every other Homelab tag, so
// Homelab can mint keys for them.
const ManagerTag = "tag:homelab"

// Homelab shares the tailnet's policy file with whoever else uses the tailnet. Everything
// Homelab writes is named hl-...: groups group:hl-*, tags tag:hl-*, and grants that only
// name those (plus autogroup:admin). Homelab replaces only those parts and leaves the rest
// of the file, comments included, as it was. It also adds tag:homelab if it's missing.
const (
	groupPrefix = "group:hl-"
	tagPrefix   = "tag:hl-"
)

type policyGrant struct {
	Src []string `json:"src"`
	Dst []string `json:"dst"`
	IP  []string `json:"ip"`
}

// homelabPart is Homelab's share of the policy file.
type homelabPart struct {
	groups    map[string][]string
	tagOwners map[string][]string
	grants    []policyGrant
}

// part turns p into the groups, tag owners and grants Homelab writes. The output is
// sorted, so the same policy always gives the same file.
func part(p core.AccessPolicy) (homelabPart, error) {
	hp := homelabPart{groups: map[string][]string{}, tagOwners: map[string][]string{}}
	for name, logins := range p.Groups {
		if !groupNameRe.MatchString(name) || !strings.HasPrefix("group:"+name, groupPrefix) || "group:"+name == groupPrefix {
			return hp, fmt.Errorf("%q can't be used as a group name; it should start with hl- and use only lowercase letters, numbers and dashes", name)
		}
		members := []string{}
		for _, l := range logins {
			l = strings.TrimSpace(l)
			if l == "" {
				return hp, fmt.Errorf("group %q has a member with no email address", name)
			}
			members = append(members, l)
		}
		hp.groups["group:"+name] = sortedUnique(members)
	}
	var tags []string
	for _, t := range p.TagOwners {
		if t == ManagerTag {
			continue
		}
		if !tagRe.MatchString(t) || !strings.HasPrefix(t, tagPrefix) || t == tagPrefix {
			return hp, fmt.Errorf("%q can't be used as a tag; it should look like tag:hl-name with only lowercase letters, numbers and dashes", t)
		}
		hp.tagOwners[t] = []string{"autogroup:admin", ManagerTag}
		tags = append(tags, t)
	}
	tags = sortedUnique(tags)
	if len(tags) > 0 {
		hp.grants = append(hp.grants, policyGrant{Src: []string{"autogroup:admin"}, Dst: tags, IP: []string{"*"}})
	}
	var grants []policyGrant
	for _, g := range p.Grants {
		var src, dst []string
		for _, name := range g.Groups {
			if _, ok := hp.groups["group:"+name]; !ok {
				return hp, fmt.Errorf("an access rule names the group %q, which doesn't exist", name)
			}
			src = append(src, "group:"+name)
		}
		for _, t := range g.Tags {
			if _, ok := hp.tagOwners[t]; !ok {
				return hp, fmt.Errorf("an access rule names the tag %q, which Homelab doesn't manage", t)
			}
			dst = append(dst, t)
		}
		if len(src) == 0 || len(dst) == 0 {
			continue
		}
		grants = append(grants, policyGrant{Src: sortedUnique(src), Dst: sortedUnique(dst), IP: []string{"*"}})
	}
	sort.Slice(grants, func(i, j int) bool {
		return strings.Join(grants[i].Src, ",")+"|"+strings.Join(grants[i].Dst, ",") <
			strings.Join(grants[j].Src, ",")+"|"+strings.Join(grants[j].Dst, ",")
	})
	hp.grants = append(hp.grants, grants...)
	return hp, nil
}

// MergePolicy returns the policy file cur (HuJSON) with Homelab's part replaced by p.
// Anything that isn't Homelab's, such as other people's rules, ssh, tests and comments,
// is kept.
func MergePolicy(cur []byte, p core.AccessPolicy) ([]byte, error) {
	hp, err := part(p)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(cur)) == 0 {
		cur = []byte("{}")
	}
	v, err := hujson.Parse(cur)
	if err != nil {
		return nil, fmt.Errorf("Homelab couldn't read the tailnet's access rules: %w", err)
	}
	root, ok := v.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("the tailnet's access rules aren't a JSON object")
	}

	groups, err := section(root, "groups", '{')
	if err != nil {
		return nil, err
	}
	keepMembers(groups.(*hujson.Object), func(name string) bool { return !strings.HasPrefix(name, groupPrefix) })
	for _, name := range sortedKeys(hp.groups) {
		if err := addMember(groups.(*hujson.Object), name, hp.groups[name]); err != nil {
			return nil, err
		}
	}

	owners, err := section(root, "tagOwners", '{')
	if err != nil {
		return nil, err
	}
	obj := owners.(*hujson.Object)
	hasManager := false
	for _, m := range obj.Members {
		if memberName(m) == ManagerTag {
			hasManager = true
		}
	}
	keepMembers(obj, func(name string) bool { return !strings.HasPrefix(name, tagPrefix) })
	if !hasManager {
		if err := addMember(obj, ManagerTag, []string{"autogroup:admin"}); err != nil {
			return nil, err
		}
	}
	for _, t := range sortedKeys(hp.tagOwners) {
		if err := addMember(obj, t, hp.tagOwners[t]); err != nil {
			return nil, err
		}
	}

	grants, err := section(root, "grants", '[')
	if err != nil {
		return nil, err
	}
	arr := grants.(*hujson.Array)
	keepElements(arr, func(e hujson.Value) bool { return !isHomelabGrant(e) })
	for _, g := range hp.grants {
		val, err := jsonValue(g)
		if err != nil {
			return nil, err
		}
		arr.Elements = append(arr.Elements, val)
	}

	v.Format()
	return v.Pack(), nil
}

// section finds the top-level member name, adding it (empty) if it's missing.
func section(root *hujson.Object, name string, kind hujson.Kind) (hujson.ValueTrimmed, error) {
	for i := range root.Members {
		if memberName(root.Members[i]) != name {
			continue
		}
		val := root.Members[i].Value.Value
		if val.Kind() != kind {
			return nil, fmt.Errorf("%q in the tailnet's access rules has an unexpected shape", name)
		}
		return val, nil
	}
	var val hujson.ValueTrimmed = &hujson.Object{}
	if kind == '[' {
		val = &hujson.Array{}
	}
	root.Members = append(root.Members, hujson.ObjectMember{
		Name:  hujson.Value{Value: hujson.String(name)},
		Value: hujson.Value{Value: val},
	})
	return val, nil
}

func memberName(m hujson.ObjectMember) string {
	if lit, ok := m.Name.Value.(hujson.Literal); ok {
		return lit.String()
	}
	return ""
}

func addMember(obj *hujson.Object, name string, value any) error {
	val, err := jsonValue(value)
	if err != nil {
		return err
	}
	obj.Members = append(obj.Members, hujson.ObjectMember{Name: hujson.Value{Value: hujson.String(name)}, Value: val})
	return nil
}

func jsonValue(x any) (hujson.Value, error) {
	b, err := json.Marshal(x)
	if err != nil {
		return hujson.Value{}, err
	}
	return hujson.Parse(b)
}

// keepMembers drops the members keep says no to. Comments in front of a dropped member
// move to the next member, so other people's comments are never lost.
func keepMembers(obj *hujson.Object, keep func(name string) bool) {
	var out []hujson.ObjectMember
	var carry hujson.Extra
	for _, m := range obj.Members {
		if !keep(memberName(m)) {
			carry = append(carry, comments(m.Name.BeforeExtra)...)
			continue
		}
		m.Name.BeforeExtra = append(append(hujson.Extra{}, carry...), m.Name.BeforeExtra...)
		carry = nil
		out = append(out, m)
	}
	obj.AfterExtra = append(carry, obj.AfterExtra...)
	obj.Members = out
}

func keepElements(arr *hujson.Array, keep func(hujson.Value) bool) {
	var out []hujson.Value
	var carry hujson.Extra
	for _, e := range arr.Elements {
		if !keep(e) {
			carry = append(carry, comments(e.BeforeExtra)...)
			continue
		}
		e.BeforeExtra = append(append(hujson.Extra{}, carry...), e.BeforeExtra...)
		carry = nil
		out = append(out, e)
	}
	arr.AfterExtra = append(carry, arr.AfterExtra...)
	arr.Elements = out
}

// comments keeps extra only if it holds a comment; plain whitespace is dropped.
func comments(extra hujson.Extra) hujson.Extra {
	if bytes.Contains(extra, []byte("//")) || bytes.Contains(extra, []byte("/*")) {
		return append(hujson.Extra{}, extra...)
	}
	return nil
}

// isHomelabGrant says whether e is a grant Homelab wrote: exactly src, dst and ip, with
// every src one of Homelab's groups or autogroup:admin and every dst one of its tags.
func isHomelabGrant(e hujson.Value) bool {
	v := e.Clone()
	v.Standardize()
	var g map[string]any
	if json.Unmarshal(v.Pack(), &g) != nil || len(g) != 3 {
		return false
	}
	src, ok1 := stringList(g["src"])
	dst, ok2 := stringList(g["dst"])
	if _, ok := g["ip"]; !ok || !ok1 || !ok2 || len(src) == 0 || len(dst) == 0 {
		return false
	}
	for _, s := range src {
		if s != "autogroup:admin" && !strings.HasPrefix(s, groupPrefix) {
			return false
		}
	}
	for _, d := range dst {
		if !strings.HasPrefix(d, tagPrefix) {
			return false
		}
	}
	return true
}

func stringList(x any) ([]string, bool) {
	list, ok := x.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedUnique(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if i == 0 || s != out[n-1] {
			out[n] = s
			n++
		}
	}
	return out[:n]
}

// normalizeJSON re-marshals raw (JSON or HuJSON) so comments, formatting and key order don't matter when comparing.
func normalizeJSON(raw []byte) (string, bool) {
	std, err := hujson.Standardize(append([]byte{}, raw...)) // Standardize reuses its input
	if err != nil {
		return "", false
	}
	var v any
	if err := json.Unmarshal(std, &v); err != nil {
		return "", false
	}
	out, err := json.Marshal(v)
	return string(out), err == nil
}
