package secret

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

var secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Delivery contains plaintext for consumers and metadata for receipts.
// Use it only when Resolve succeeds; do not log or persist ByConsumer.
type Delivery struct {
	// ByConsumer: part name or "init-<name>" → env to inject. Only consumers
	// that receive something appear.
	ByConsumer map[string]map[string]string
	Receipts   []docker.SecretReceipt
	Profile    string
}

// EnvFor is what a target driver's renderer asks of a delivery.
func (d Delivery) EnvFor(consumer string) map[string]string {
	if d.ByConsumer == nil {
		return nil
	}
	return d.ByConsumer[consumer]
}

// --- bindings and profiles ------------------------------------------------------

// DeploymentProfile is the one profile a deployment uses, for app.yaml and
// secretspec.toml alike (appcontract.DeploymentProfile): the explicit
// choice when either file declares it (default always counts), else
// preview for a preview when either declares it, else default. spec and c
// may each be nil (a file the app does not have). A choice that cannot be
// read is an error, never the default: the default may hold less memory
// than the deployment was given.
func (a *Service) DeploymentProfile(ctx context.Context, projectID ids.ID, deployment string, spec *Spec, c *appcontract.Contract) (string, appcontract.ProfileChoice, error) {
	chosen, err := a.Profiles.Chosen(ctx, projectID, deployment)
	if err != nil {
		return "", "", fmt.Errorf("reading the profile chosen for the %s deployment: %w", deployment, err)
	}
	name, how := appcontract.DeploymentProfile(chosen, deployment, DeclaredProfiles(spec, c))
	return name, how, nil
}

// DeclaredProfiles is every profile name either file declares, default
// among them.
func DeclaredProfiles(spec *Spec, c *appcontract.Contract) map[string]bool {
	declared := map[string]bool{appcontract.DefaultProfile: true}
	if spec != nil {
		for _, n := range spec.ProfileNames() {
			declared[n] = true
		}
	}
	if c != nil {
		for _, n := range c.ProfileNames() {
			declared[n] = true
		}
	}
	return declared
}

// SpecProfile is the SecretSpec profile a deployment's profile applies:
// the profile itself when the file declares it, else its default, else
// its first profile in name order (a SecretSpec need not declare default;
// such a file has always used its first). spec must contain at least one
// profile, as guaranteed by ParseSpec.
func SpecProfile(spec *Spec, profile string) string {
	if _, ok := spec.Profiles[profile]; ok {
		return profile
	}
	if _, ok := spec.Profiles[appcontract.DefaultProfile]; ok {
		return appcontract.DefaultProfile
	}
	return spec.ProfileNames()[0]
}

// --- resolution -----------------------------------------------------------------

// MissingError names the required secrets nothing provides. The
// sentence points at the page that fixes it. Consumers are the parts and
// init steps (as init-<name>) those secrets would reach.
type MissingError struct {
	Deployment string
	Names      []string
	Consumers  []string
}

func (e *MissingError) Error() string {
	it := "it"
	if len(e.Names) > 1 {
		it = "them"
	}
	return fmt.Sprintf("this app needs %s in the %s deployment and nothing provides %s — set %s under Settings → Secrets",
		strings.Join(e.Names, ", "), e.Deployment, it, it)
}

// UnavailableError is a bound secret whose store, or a deployment whose
// bindings, could not be read. It says nothing about whether the value
// exists. Its sentence names the secret and the store, never the store's
// own error, which may carry an endpoint; Unwrap keeps that for callers
// that log privately.
type UnavailableError struct {
	Deployment string
	Name       string // "" when the bindings themselves could not be read
	Store      string
	cause      error
}

func (e *UnavailableError) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("the secret bindings of the %s deployment could not be read", e.Deployment)
	}
	return fmt.Sprintf("%s in the %s deployment is bound to the %s store, which could not be read", e.Name, e.Deployment, e.Store)
}
func (e *UnavailableError) Unwrap() error         { return e.cause }
func (e *UnavailableError) FaultKind() fault.Kind { return fault.Unavailable }

// Resolve prepares application environment values from ws and its nonnil contract c.
// The caller selects an authorized project/deployment and the source tree to boot.
// No declaration returns an empty delivery unless c declares generated secrets.
// Resolution may persist generated values and bindings before a later error;
// it is not atomic as a whole. Discard the returned Delivery on error.
func (a *Service) Resolve(ctx context.Context, projectID ids.ID, slug, deployment, ws string, c *appcontract.Contract) (Delivery, error) {
	return a.resolve(ctx, projectID, slug, deployment, ws, c, true)
}

// Check is Resolve's judgement without its effects: the same declaration,
// profile, scopes, bindings and stores, with a generated secret that is not
// yet bound counted as provided (Resolve mints it) rather than minted. It
// answers nil, a [MissingError], an [UnavailableError], or a refusal of the
// declaration (fault.Invalid). It reads values only to prove they exist and
// returns none. A pass is true when it is asked; Resolve judges again at
// delivery.
func (a *Service) Check(ctx context.Context, projectID ids.ID, slug, deployment, ws string, c *appcontract.Contract) error {
	_, err := a.resolve(ctx, projectID, slug, deployment, ws, c, false)
	return err
}

// resolve is Resolve, and with mint false Check: the one rule for both.
func (a *Service) resolve(ctx context.Context, projectID ids.ID, slug, deployment, ws string, c *appcontract.Contract, mint bool) (Delivery, error) {
	var d Delivery
	spec, present, err := ReadSpec(ws, c.SecretSpecPath)
	if err != nil {
		return d, err
	}
	if !present {
		if len(c.SecretGenerate) > 0 {
			return d, fault.Wrap(fault.Invalid, fmt.Errorf(".apptown/app.yaml declares secrets: but the app has no %s", SpecFile))
		}
		return d, nil
	}
	if err := spec.CheckScopesAgainst(c); err != nil {
		return d, fault.Wrap(fault.Invalid, err)
	}
	chosen, _, err := a.DeploymentProfile(ctx, projectID, deployment, spec, c)
	if err != nil {
		return d, err
	}
	profile := SpecProfile(spec, chosen)
	d.Profile = profile
	d.ByConsumer = map[string]map[string]string{}
	bindings, err := a.Bindings.For(ctx, projectID, deployment)
	if err != nil {
		return Delivery{}, &UnavailableError{Deployment: deployment, cause: err}
	}

	var infValues map[string]string
	var missing, starved []string
	for _, s := range spec.SecretsIn(profile) {
		consumers, all := spec.ConsumersOf(s.Name)
		if all {
			consumers = allConsumers(c)
		}
		b, bound := bindings[s.Name]
		if !bound && s.Generate && !mint {
			continue // provided: Resolve mints and binds it on first need
		}
		if !bound && s.Generate {
			// A generated secret binds itself on first need (§8): minted
			// once per stability domain, never regenerated on deploy.
			// Only a value the store says it does not hold is minted: a
			// read that fails is unknown, and minting then would rotate it.
			domain := c.SecretGenerate[s.Name]
			scope := Scope(slug, deployment, domain)
			_, ver, err := a.Vault.Get(ctx, scope, s.Name)
			if errors.Is(err, ErrNotStored) {
				v, err := GenerateValue()
				if err != nil {
					return d, err
				}
				if ver, err = a.Vault.Put(ctx, scope, s.Name, v, "apptown-system"); err != nil {
					return d, err
				}
				a.record(ctx, "apptown-system", projectID, "secret.generated", s.Name,
					map[string]any{"deployment": deployment, "scope": scope})
			} else if err != nil {
				return Delivery{}, &UnavailableError{Deployment: deployment, Name: s.Name, Store: StoreInternal, cause: err}
			}
			b = Binding{Deployment: deployment, Name: s.Name, Store: StoreInternal, Ref: scope,
				Version: fmt.Sprint(ver), Generated: true, UpdatedBy: "apptown-system"}
			if err := a.Bindings.Put(ctx, projectID, b); err != nil {
				return d, err
			}
			bound = true
		}
		var value, version string
		switch {
		case !bound:
			if s.Default != "" {
				value, version = s.Default, "default"
			} else if s.Required {
				missing, starved = append(missing, s.Name), append(starved, consumers...)
				continue
			} else {
				continue
			}
		default:
			var ok bool
			value, version, ok, err = a.boundValue(ctx, deployment, b, s.Name, &infValues)
			if err != nil {
				return d, err
			}
			if !ok {
				missing, starved = append(missing, s.Name), append(starved, consumers...)
				continue
			}
		}
		if !mint {
			continue // its value exists; Check delivers nothing
		}
		for _, consumer := range consumers {
			if d.ByConsumer[consumer] == nil {
				d.ByConsumer[consumer] = map[string]string{}
			}
			d.ByConsumer[consumer][s.Name] = value
		}
		d.Receipts = append(d.Receipts, docker.SecretReceipt{Name: s.Name, Store: b.Store, Version: version,
			Consumers: consumers, Generated: b.Generated})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		slices.Sort(starved)
		return d, &MissingError{Deployment: deployment, Names: missing, Consumers: slices.Compact(starved)}
	}
	return d, nil
}

// boundValue reads a bound secret's value from its store. ok is false when
// the built-in store holds nothing for it; a store that cannot be read is
// an UnavailableError, and an Infisical key that is missing is an error.
// inf caches Infisical's values across one resolution.
func (a *Service) boundValue(ctx context.Context, deployment string, b Binding, name string, inf *map[string]string) (value, version string, ok bool, err error) {
	switch b.Store {
	case StoreInternal:
		v, ver, err := a.Vault.Get(ctx, b.Ref, name)
		if errors.Is(err, ErrNotStored) {
			return "", "", false, nil
		}
		if err != nil {
			return "", "", false, &UnavailableError{Deployment: deployment, Name: name, Store: StoreInternal, cause: err}
		}
		return v, fmt.Sprint(ver), true, nil
	case StoreInfisical:
		if *inf == nil {
			if *inf, err = a.InfisicalValues(ctx); err != nil {
				return "", "", false, &UnavailableError{Deployment: deployment, Name: name, Store: StoreInfisical, cause: err}
			}
		}
		v, ok := (*inf)[b.Ref]
		if !ok || v == "" {
			return "", "", false, fault.Wrap(fault.Invalid, fmt.Errorf("%s is bound to Infisical key %s, which does not exist there", name, b.Ref))
		}
		return v, "infisical", true, nil
	}
	return "", "", false, fault.Wrap(fault.Invalid, fmt.Errorf("%s is bound to an unknown store %q", name, b.Store))
}

func allConsumers(c *appcontract.Contract) []string {
	var out []string
	for _, p := range c.Parts {
		out = append(out, p.Name)
	}
	for _, it := range c.Init {
		out = append(out, "init-"+it.Name)
	}
	return out
}

// delivery is the broker's answer as the built-in host asks for it.
func (d Delivery) Docker() docker.Delivery {
	return docker.Delivery{Env: d.EnvFor, Receipts: d.Receipts, Profile: d.Profile}
}

// ValidName is the declaration and binding command name rule.
func ValidName(name string) bool { return secretNameRe.MatchString(name) }
