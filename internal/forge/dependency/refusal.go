package dependency

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// RefusesNeeds says why a contract's declared needs cannot be fulfilled as
// declared, in a sentence naming what to change, or "" (the needs
// contract, services-and-data guide, "What each need gives the app"). It is asked
// before any effect wherever a contract is checked, released or booted, so
// a contract is refused the same way everywhere:
//
//   - a kind whose values come from the project's attached integration
//     (error reporting, sign-in, telemetry) is not a need;
//   - a value the forge owns (a database password) is not the app's to set;
//   - two needs that give the app the same variable would give one name two
//     meanings.
//
// A need the catalogue does not hold, or holds unreadable, is refused first,
// in the one sentence every surface uses (UnknownNeeds).
func (c *Catalog) RefusesNeeds(ctx context.Context, needs []string, params map[string]map[string]string) (string, error) {
	gives := map[string][]string{} // variable -> needs that give it
	var unknown []string
	kinds := map[string]*Kind{}
	for _, need := range needs {
		k, err := c.ByCapability(ctx, need)
		if errors.Is(err, ErrUnknown) {
			unknown = append(unknown, need)
			continue
		}
		if errors.Is(err, ErrSpec) {
			return fmt.Sprintf("this app needs %s, whose catalogue entry cannot be read: %v", need, err), nil
		}
		if err != nil {
			return "", fmt.Errorf("reading the dependency kind %s: %w", need, err)
		}
		kinds[need] = k
	}
	if len(unknown) > 0 {
		return (&UnknownNeeds{Needs: unknown}).Error(), nil
	}
	for _, need := range needs {
		k := kinds[need]
		if why := k.RefusesDeclaration(params[need]); why != "" {
			return why, nil
		}
		for _, name := range k.Names() {
			gives[name] = append(gives[name], need)
		}
	}
	var clashes []string
	for name, by := range gives {
		if len(by) > 1 {
			clashes = append(clashes, fmt.Sprintf("%s is given by %s", name, strings.Join(by, " and ")))
		}
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		return "these needs give the app the same variable, so one name would have two meanings: " + strings.Join(clashes, "; ") + ". Declare only one of them", nil
	}
	return "", nil
}

// RefusesDeclaration says why the kind cannot be declared as a need with
// these parameters, or "".
func (k *Kind) RefusesDeclaration(params map[string]string) string {
	if k.Integration {
		return fmt.Sprintf("%s is an integration, not a need: attach it to the project under Settings › Integrations and remove %s from needs: in .forge/app.yaml; the app is given its values while it is attached and must run without them", k.Capability, k.Capability)
	}
	var owned []string
	for name := range params {
		if _, ok := k.Credentials[name]; ok {
			owned = append(owned, name)
		}
	}
	if len(owned) > 0 {
		slices.Sort(owned)
		return fmt.Sprintf("the forge sets the %s of the %s need, and an app cannot: remove %s from needs.%s in .forge/app.yaml and read the value from the variables the app is given", strings.Join(owned, " and "), k.Capability, strings.Join(owned, ", "), k.Capability)
	}
	return ""
}

// UnknownNeeds is a contract's needs the catalogue does not hold.
type UnknownNeeds struct{ Needs []string }

func (e *UnknownNeeds) Error() string {
	return "this app needs " + strings.Join(e.Needs, ", ") + ", which is not in the dependency catalog"
}

// Refused is a contract's needs refused as declared (RefusesNeeds).
type Refused struct{ Why string }

func (e *Refused) Error() string { return e.Why }
