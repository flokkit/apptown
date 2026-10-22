package dependency

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// RefusesNeeds says why a contract's declared needs cannot be fulfilled as
// declared, in a sentence naming what to change, or "" (the needs
// contract, services-and-data guide, "What each need gives the app"). It is asked
// before any effect wherever a contract is checked, released or booted, so
// a contract is refused the same way everywhere (an agent step asked to
// change the app is told the sentence instead, so a change can fix it):
//
//   - a kind whose values come from the project's attached integration
//     (error reporting, sign-in, telemetry) is not a need;
//   - a value AppTown owns (a database password) is not the app's to set;
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
		return fmt.Sprintf("%s is an integration, not a need: attach it to the project under Settings › Integrations and remove %s from needs: in .apptown/app.yaml; the app is given its values while it is attached and must run without them", k.Capability, k.Capability)
	}
	var owned []string
	for name := range params {
		if _, ok := k.Credentials[name]; ok {
			owned = append(owned, name)
		}
	}
	if len(owned) > 0 {
		slices.Sort(owned)
		return fmt.Sprintf("AppTown sets the %s of the %s need, and an app cannot: remove %s from needs.%s in .apptown/app.yaml and read the value from the variables the app is given", strings.Join(owned, " and "), k.Capability, strings.Join(owned, ", "), k.Capability)
	}
	// A parameter the kind does not take would do nothing today and
	// something once the kind learns it: refused, never ignored.
	accepted := k.AcceptedParameters()
	var unknown []string
	for name := range params {
		if !slices.Contains(accepted, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Sprintf("the %s need does not take %s: remove %s from needs.%s in .apptown/app.yaml (it takes %s, size and resources)", k.Capability, strings.Join(unknown, " or "), strings.Join(unknown, ", "), k.Capability, strings.Join(accepted, ", "))
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

// RefusesNeedRefs is the catalogue's refusal of a contract's need-value
// references (appcontract.NeedRef, contract version 2): each must name a
// variable its need documents (provides), and a register-only need's values,
// which arrive by registration rather than as a connection, cannot be
// referenced yet; the app reads those variables by their own names.
func (c *Catalog) RefusesNeedRefs(ctx context.Context, refs []appcontract.NeedRef) (string, error) {
	for _, ref := range refs {
		k, err := c.ByCapability(ctx, ref.Need)
		if errors.Is(err, ErrUnknown) || errors.Is(err, ErrSpec) {
			continue // RefusesNeeds says so in its own sentence
		}
		if err != nil {
			return "", fmt.Errorf("reading the dependency kind %s: %w", ref.Need, err)
		}
		if k.Local.Image == "" {
			return fmt.Sprintf("%s: environment: %s refers to %s's %s; a %s need's values cannot be referenced yet: read %s by its own name", ref.Part, ref.Key, ref.Need, ref.Var, ref.Need, ref.Var), nil
		}
		if _, ok := k.Provides[ref.Var]; !ok {
			names := make([]string, 0, len(k.Provides))
			for n := range k.Provides {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Sprintf("%s: environment: %s refers to %s's %s, which %s does not give; it gives %s", ref.Part, ref.Key, ref.Need, ref.Var, ref.Need, strings.Join(names, ", ")), nil
		}
	}
	return "", nil
}
