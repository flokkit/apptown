package forge

import (
	"fmt"
	"sort"
	"strings"
)

// Feature flags: switches an operator turns on for one
// forge, for things that must exist but must not be reachable by default.
// The first is the development convenience of deleting conformance
// history. A flag is named at boot (-features a,b or FORGE_FEATURES); an
// unknown name is refused loudly so a typo cannot silently leave a feature
// off. Flags are not policy and not ways of working: they are the
// operator's own switches for this installation.
var knownFeatures = map[string]string{
	"dev-tools": "development conveniences: deleting all conformance history from the Conformance page",
}

// parseFeatures validates and allocates an instance-owned flag set.
func parseFeatures(list string) (map[string]bool, error) {
	on := map[string]bool{}
	for _, f := range strings.Split(list, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, ok := knownFeatures[f]; !ok {
			names := make([]string, 0, len(knownFeatures))
			for k := range knownFeatures {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("unknown feature %q (known: %s)", f, strings.Join(names, ", "))
		}
		on[f] = true
	}
	return on, nil
}

// Feature reports whether a flag is on for this forge.
func (a *App) Feature(name string) bool { return a.cfg.features[name] }

// enabledFeatures lists the flags that are on, sorted, for the API.
func (a *App) enabledFeatures() []string {
	out := []string{}
	for k := range a.cfg.features {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
