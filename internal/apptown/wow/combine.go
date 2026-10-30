package wow

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

// Combined is what several packs give a project together: the union of
// their setup (design/subsystems/ways-of-working-versions.md). There is no
// precedence between packs: where two disagree on something a project can
// have only one of, Problems names both and the combination is not adopted.
// Guidance and workflow pins are compared where adoption reads them.
type Combined struct {
	Packs      []string             `json:"packs"`
	Toolchain  string               `json:"toolchain,omitempty"`
	Tools      map[string]string    `json:"tools,omitempty"`
	Services   []deploy.ServiceSpec `json:"services"`
	Policies   []PackPolicy         `json:"policies"`
	Extensions []string             `json:"extensions"`
	Requires   []string             `json:"requires"`
	// Starter is the one pack among them with starter files, if any.
	Starter  string   `json:"starter,omitempty"`
	Problems []string `json:"problems"`
}

// Combine unions packs' setup. hasStarter says whether a pack carries
// starter files; nil says none does.
func Combine(packs []Pack, hasStarter func(string) bool) Combined {
	out := Combined{Packs: []string{}, Tools: map[string]string{}, Services: []deploy.ServiceSpec{}, Policies: []PackPolicy{},
		Extensions: []string{}, Requires: []string{}, Problems: []string{}}
	packs = slices.Clone(packs)
	slices.SortFunc(packs, func(a, b Pack) int { return strings.Compare(a.Name, b.Name) })
	disagree := func(a, b, what string) {
		out.Problems = append(out.Problems, fmt.Sprintf("%s and %s disagree on %s; they cannot be taken together", a, b, what))
	}
	toolFrom, serviceFrom, policyFrom := map[string]string{}, map[string]string{}, map[string]string{}
	toolchainFrom := ""
	for _, p := range packs {
		out.Packs = append(out.Packs, p.Name)
		if p.Toolchain != "" {
			if out.Toolchain != "" && out.Toolchain != p.Toolchain {
				disagree(toolchainFrom, p.Name, fmt.Sprintf("the toolchain (%s, %s)", out.Toolchain, p.Toolchain))
			} else if out.Toolchain == "" {
				out.Toolchain, toolchainFrom = p.Toolchain, p.Name
			}
		}
		for _, tool := range slices.Sorted(maps.Keys(p.Tools)) {
			line := p.Tools[tool]
			if had, seen := out.Tools[tool]; seen && had != line {
				disagree(toolFrom[tool], p.Name, fmt.Sprintf("%s's version line (%s, %s)", tool, had, line))
				continue
			}
			out.Tools[tool], toolFrom[tool] = line, p.Name
		}
		for _, svc := range p.Services {
			if from, seen := serviceFrom[svc.Name]; seen {
				i := slices.IndexFunc(out.Services, func(s deploy.ServiceSpec) bool { return s.Name == svc.Name })
				if !reflect.DeepEqual(out.Services[i], svc) {
					disagree(from, p.Name, "the "+svc.Name+" service")
				}
				continue
			}
			out.Services, serviceFrom[svc.Name] = append(out.Services, svc), p.Name
		}
		for _, pol := range p.Policies {
			if from, seen := policyFrom[pol.Name]; seen {
				i := slices.IndexFunc(out.Policies, func(q PackPolicy) bool { return q.Name == pol.Name })
				if !samePolicy(out.Policies[i], pol) {
					disagree(from, p.Name, "policy "+pol.Name)
				}
				continue
			}
			out.Policies, policyFrom[pol.Name] = append(out.Policies, pol), p.Name
		}
		out.Extensions = append(out.Extensions, p.Extensions...)
		out.Requires = append(out.Requires, p.Requires...)
		if hasStarter != nil && hasStarter(p.Name) {
			if out.Starter != "" {
				disagree(out.Starter, p.Name, "the starter files (each has its own)")
			} else {
				out.Starter = p.Name
			}
		}
	}
	slices.Sort(out.Extensions)
	out.Extensions = slices.Compact(out.Extensions)
	slices.Sort(out.Requires)
	out.Requires = slices.Compact(out.Requires)
	return out
}

// samePolicy compares a policy's meaning: its rule and options. Options
// are compared as JSON, so a YAML-read and a stored value agree.
func samePolicy(a, b PackPolicy) bool {
	with := func(m map[string]any) ([]byte, error) {
		if m == nil {
			m = map[string]any{}
		}
		return json.Marshal(m)
	}
	ja, errA := with(a.With)
	jb, errB := with(b.With)
	return a.Rule == b.Rule && errA == nil && errB == nil && string(ja) == string(jb)
}

// Of is a published spec as the pack named name: what Combine reads.
func (ps PackSpec) Of(name string) Pack {
	return Pack{Name: name, Platform: ps.Platform, Guidance: ps.Guidance, Workflows: ps.Workflows, Policies: ps.Policies,
		Toolchain: ps.Toolchain, Tools: ps.Tools, Services: ps.Services, Extensions: ps.Extensions, Requires: ps.Requires}
}
