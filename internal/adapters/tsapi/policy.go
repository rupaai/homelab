package tsapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/rupaai/homelab/internal/core"
)

var (
	groupNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	tagRe       = regexp.MustCompile(`^tag:[a-z0-9-]+$`)
)

// ManagerTag is the tag on Homelab's OAuth client. It owns every other Homelab tag, so
// Homelab can mint keys for them.
const ManagerTag = "tag:homelab"

type policyDoc struct {
	Groups    map[string][]string `json:"groups"`
	TagOwners map[string][]string `json:"tagOwners"`
	Grants    []policyGrant       `json:"grants"`
	SSH       []string            `json:"ssh"`
}

type policyGrant struct {
	Src []string `json:"src"`
	Dst []string `json:"dst"`
	IP  []string `json:"ip"`
}

// PolicyJSON renders p as a Tailscale policy file. The output is sorted, so the same policy
// always gives the same bytes. Nothing is allowed unless p grants it, apart from admins
// reaching Homelab's own tagged addresses.
func PolicyJSON(p core.AccessPolicy) ([]byte, error) {
	doc := policyDoc{
		Groups:    map[string][]string{},
		TagOwners: map[string][]string{},
		Grants:    []policyGrant{},
		SSH:       []string{},
	}
	for name, logins := range p.Groups {
		if !groupNameRe.MatchString(name) {
			return nil, fmt.Errorf("%q can't be used as a group name; use only lowercase letters, numbers and dashes", name)
		}
		members := []string{}
		for _, l := range logins {
			l = strings.TrimSpace(l)
			if l == "" {
				return nil, fmt.Errorf("group %q has a member with no email address", name)
			}
			members = append(members, l)
		}
		doc.Groups["group:"+name] = sortedUnique(members)
	}
	var tags []string
	for _, t := range p.TagOwners {
		if !tagRe.MatchString(t) {
			return nil, fmt.Errorf("%q can't be used as a tag; it should look like tag:name with only lowercase letters, numbers and dashes", t)
		}
		if t == ManagerTag {
			continue
		}
		doc.TagOwners[t] = []string{"autogroup:admin", ManagerTag}
		tags = append(tags, t)
	}
	doc.TagOwners[ManagerTag] = []string{"autogroup:admin"}
	tags = sortedUnique(tags)
	if len(tags) > 0 {
		doc.Grants = append(doc.Grants, policyGrant{Src: []string{"autogroup:admin"}, Dst: tags, IP: []string{"*"}})
	}
	var grants []policyGrant
	for _, g := range p.Grants {
		var src, dst []string
		for _, name := range g.Groups {
			if _, ok := doc.Groups["group:"+name]; !ok {
				return nil, fmt.Errorf("an access rule names the group %q, which doesn't exist", name)
			}
			src = append(src, "group:"+name)
		}
		for _, t := range g.Tags {
			if !tagRe.MatchString(t) {
				return nil, fmt.Errorf("%q can't be used as a tag; it should look like tag:name with only lowercase letters, numbers and dashes", t)
			}
			if _, ok := doc.TagOwners[t]; !ok {
				return nil, fmt.Errorf("an access rule names the tag %q, which Homelab doesn't manage", t)
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
	doc.Grants = append(doc.Grants, grants...)
	return json.MarshalIndent(doc, "", "  ")
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

// normalizeJSON re-marshals raw so formatting and key order don't matter when comparing.
func normalizeJSON(raw []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	out, err := json.Marshal(v)
	return string(out), err == nil
}
