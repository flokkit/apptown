package secret

import (
	"context"
	"fmt"
	"sort"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
)

// rehearsalProfile is the profile a restore rehearsal reads
// (design/subsystems/deployment.md): the allow-list of bound
// secrets a rehearsal may be given.
const rehearsalProfile = "rehearsal"

// ErrRehearsalInherits refuses a rehearsal profile that takes default's
// entries: an allow-list that inherits would deliver every production
// credential default names.
var ErrRehearsalInherits = fault.New(fault.Unprocessable, "the rehearsal profile in secretspec.toml inherits from default; add `defaults = { inherit = false }` under [profiles.rehearsal], so a rehearsal gets only the secrets it names")

// rehearsal is what a restore rehearsal is given and what it is not.
type rehearsal struct {
	Delivery Delivery
	// Withheld names each declared secret the rehearsal was not given.
	Withheld []string
}

// ResolveRehearsal is a restore rehearsal's delivery for an app declared
// in ws, against the bindings of the deployment it rehearses (source):
//   - a bound secret is delivered only when the app's rehearsal profile
//     names it, and that profile must not inherit (ErrRehearsalInherits);
//   - a secret whose rehearsal entry declares a default gets that value,
//     empty included, never the deployment's bound one;
//   - with no rehearsal profile no bound secret is delivered: the ordinary
//     fallback to default does not apply;
//   - every generate = true secret, in any profile, gets a value made now
//     for this rehearsal alone: never stored, never the source's;
//   - nothing is required, so a withheld secret never stops a boot.
func (a *Service) ResolveRehearsal(ctx context.Context, projectID ids.ID, source, ws string, c *appcontract.Contract) (rehearsal, error) {
	out := rehearsal{Delivery: Delivery{Profile: rehearsalProfile, ByConsumer: map[string]map[string]string{}}}
	spec, present, err := ReadSpec(ws, c.SecretSpecPath)
	if err != nil {
		return out, err
	}
	if !present {
		return out, nil
	}
	if err := spec.CheckScopesAgainst(c); err != nil {
		return out, err
	}
	allowed, hasProfile := spec.Profiles[rehearsalProfile]
	if hasProfile && spec.Inherits[rehearsalProfile] {
		return out, ErrRehearsalInherits
	}
	declared := map[string]declaredSecret{}
	for _, profile := range spec.ProfileNames() {
		for _, s := range spec.SecretsIn(profile) {
			if prev, seen := declared[s.Name]; !seen || (s.Generate && !prev.Generate) {
				declared[s.Name] = s
			}
		}
	}
	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)
	bindings, err := a.Bindings.For(ctx, projectID, source)
	if err != nil {
		return out, fmt.Errorf("reading the secret bindings of %s: %w", source, err)
	}
	var inf map[string]string
	for _, name := range names {
		s := declared[name]
		listed, isListed := allowed[name]
		var value, version, store string
		generated := false
		switch {
		case s.Generate || (isListed && listed.Generate):
			if value, err = GenerateValue(); err != nil {
				return out, err
			}
			version, store, generated = "made for this rehearsal", "rehearsal", true
		case !isListed:
			out.Withheld = append(out.Withheld, name)
			continue
		case listed.HasDefault:
			// The profile's own value is the rehearsal's, bound or not: a
			// stand-in keeps the deployment's credential out of it.
			value, version, store = listed.Default, "default", "default"
		default:
			b, bound := bindings[name]
			if !bound {
				out.Withheld = append(out.Withheld, name)
				continue
			}
			var ok bool
			if value, version, ok, err = a.boundValue(ctx, b.Deployment, b, name, &inf); err != nil {
				return out, err
			}
			if !ok {
				out.Withheld = append(out.Withheld, name)
				continue
			}
			store = b.Store
		}
		consumers, all := spec.ConsumersOf(name)
		if all {
			consumers = allConsumers(c)
		}
		for _, consumer := range consumers {
			if out.Delivery.ByConsumer[consumer] == nil {
				out.Delivery.ByConsumer[consumer] = map[string]string{}
			}
			out.Delivery.ByConsumer[consumer][name] = value
		}
		out.Delivery.Receipts = append(out.Delivery.Receipts, docker.SecretReceipt{Name: name, Store: store, Version: version, Consumers: consumers, Generated: generated})
	}
	return out, nil
}
