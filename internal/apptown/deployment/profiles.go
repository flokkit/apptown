package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// AppliedProfile is what one generation applied of its deployment's profile and
// of any operator override:
// kept with the generation's inputs, so a rebuild or a rehearsal of that
// generation applies the same, and the deployment's page says what runs.
// The zero value is a generation from before it was kept: not recorded,
// which is not "no limit".
type AppliedProfile struct {
	Services map[string]AppliedPart    `json:"services,omitempty"`
	Profile  string                    `json:"profile,omitempty"`
	How      appcontract.ProfileChoice `json:"how,omitempty"`
	Parts    map[string]AppliedPart    `json:"parts,omitempty"`
}

// AppliedPart is one part as its generation ran it.
type AppliedPart struct {
	Size            *appcontract.NeedSize `json:"size,omitempty"`
	MemoryDefault   *MemoryDefaultSource  `json:"memoryDefault,omitempty"`
	MemoryUnlimited bool                  `json:"memoryUnlimited,omitempty"`
	// Resources is the effective cap: profile, override and fresh default
	// resolution. A recorded zero field is no limit.
	Resources appcontract.Resources `json:"resources,omitzero"`
	// Env is what the profile changed of the part's environment.
	Env map[string]string `json:"env,omitempty"`
	// Override is what an operator's override set, zero when none.
	Override appcontract.Resources `json:"override,omitzero"`
	// Public marks the front door: its container is the deployment's own.
	Public bool `json:"public,omitempty"`
	// RunsOn is what the target ran the part on when it rounds a
	// declaration to its own sizes: the size's slug first, then its
	// capacity or why it was chosen in words ("apps-s-1vcpu-1gb (1G, 1
	// CPU)"); "" where the declaration is held exactly.
	RunsOn string `json:"runsOn,omitempty"`
	// Need marks a need's service rather than an app part: sized as a
	// part is, its container the stack's service of that name.
	Need bool `json:"need,omitempty"`
}

// Recorded says whether the generation kept what it applied.
func (a AppliedProfile) Recorded() bool { return a.Profile != "" }

func (a AppliedProfile) json() (json.RawMessage, error) {
	if !a.Recorded() {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(a)
}

func appliedFrom(raw json.RawMessage) (AppliedProfile, error) {
	var a AppliedProfile
	if len(raw) == 0 {
		return a, nil
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return AppliedProfile{}, fmt.Errorf("reading what the generation applied: %w", err)
	}
	return a, nil
}

// On is c as the generation ran it: each part's resources exactly as
// recorded, and the environment the profile changed merged over the
// part's own. A record from before it was kept preserves previous behavior,
// clearing newly meaningful source size selectors.
func (a AppliedProfile) On(c *appcontract.Contract) *appcontract.Contract {
	if c == nil {
		return nil
	}
	if !a.Recorded() {
		// Before profiles were recorded, size was an ignored free scalar. A
		// rebuild must not reinterpret it against today's catalogue. Retain
		// the earlier behavior of every other source field.
		if len(c.NeedSizes) == 0 {
			return c
		}
		out := *c
		out.NeedSizes = nil
		return &out
	}
	out := *c
	out.Parts = append([]appcontract.Part(nil), c.Parts...)
	for i := range out.Parts {
		p, ok := a.Parts[out.Parts[i].Name]
		if !ok {
			continue
		}
		out.Parts[i].Resources = p.Resources
		if len(p.Env) > 0 {
			out.Parts[i].Env = appcontract.MergeEnv(out.Parts[i].Env, p.Env)
		}
	}
	// A need runs as the generation ran it: the size it recorded, and no
	// limit when it recorded none, which is how every need ran before
	// needs could be sized.
	out.NeedResources = nil
	out.NeedSizes = nil
	for n, p := range a.Parts {
		if p.Need && p.Size != nil {
			if out.NeedSizes == nil {
				out.NeedSizes = map[string]appcontract.NeedSize{}
			}
			size := *p.Size
			size.Command = slices.Clone(size.Command)
			out.NeedSizes[n] = size
		}
	}
	for _, n := range c.Needs {
		if p, ok := a.Parts[n]; ok && p.Need && p.Resources.Declared() {
			if out.NeedResources == nil {
				out.NeedResources = map[string]appcontract.Resources{}
			}
			out.NeedResources[n] = p.Resources
		}
	}
	return &out
}

// Override is an operator's override of one part's resources.
type Override struct {
	MemoryUnlimited bool                  `json:"memoryUnlimited,omitempty"`
	Part            string                `json:"part"`
	Resources       appcontract.Resources `json:"resources"`
	Reason          string                `json:"reason"`
	SetBy           string                `json:"setBy"`
	SetAt           string                `json:"setAt"`
}

// Overrides are a deployment's operator overrides by part.
func (s *Service) Overrides(ctx context.Context, deploymentID ids.ID) (map[string]Override, error) {
	rows, err := deploymentsql.New(s.db.SQL()).DeploymentOverrides(ctx, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("reading the deployment's overrides: %w", err)
	}
	out := make(map[string]Override, len(rows))
	for _, r := range rows {
		out[r.Part] = Override{MemoryUnlimited: r.MemoryUnlimited, Part: r.Part, Resources: appcontract.Resources{MemoryBytes: r.MemoryBytes, CPUs: r.Cpus},
			Reason: r.Reason, SetBy: r.SetBy, SetAt: r.SetAt.UTC().Format("2006-01-02T15:04:05Z")}
	}
	return out, nil
}

// deploymentProfile is the one profile dep uses, read from the tree ws
// holds: the secret owner's selection, for app.yaml and secretspec.toml
// alike. A choice or declaration that cannot be read is an error.
func (e *Environment) deploymentProfile(ctx context.Context, projectID ids.ID, deployment, ws string, c *appcontract.Contract) (string, appcontract.ProfileChoice, error) {
	spec, present, err := secret.ReadSpec(ws, c.SecretSpecPath)
	if err != nil {
		return "", "", err
	}
	if !present {
		spec = nil
	}
	return e.secrets.DeploymentProfile(ctx, projectID, deployment, spec, c)
}

// Profiled is c under the profile the deployment named uses, and that
// profile: what a preview boots (the preview deployment's profile), and
// the start of a standing deployment's effective contract.
func (e *Environment) Profiled(ctx context.Context, projectID ids.ID, deployment, ws string, c *appcontract.Contract) (*appcontract.Contract, AppliedProfile, error) {
	name, how, err := e.deploymentProfile(ctx, projectID, deployment, ws, c)
	if err != nil {
		return nil, AppliedProfile{}, err
	}
	out := c.ForProfile(name)
	if e.needs != nil {
		out, err = e.needs.ResolveSizes(ctx, out)
		if err != nil {
			return nil, AppliedProfile{}, err
		}
	}
	serviceNeeds, err := e.serviceNeeds(ctx, out.Needs)
	if err != nil {
		return nil, AppliedProfile{}, err
	}
	// A size declared for a need that runs no service (a key AppTown
	// registers) has nothing to hold it: refused here, before a preview
	// replaces the one it had or a release registers anything.
	for _, n := range out.Needs {
		if _, declared := out.NeedResources[n]; declared && !serviceNeeds[n] {
			return nil, AppliedProfile{}, fault.New(fault.Invalid, fmt.Sprintf("needs: %s declares resources, but %s runs no service to hold to them; remove them", n, n))
		}
	}
	return out, appliedOf(out, c, name, how, nil, serviceNeeds), nil
}

// serviceNeeds are which of needs run as a service beside the app (an
// emulator on a Docker host, a managed database in a cloud): the needs a
// generation's record sizes and the deployment's page shows. A key the
// AppTown registers runs nothing and has nothing to size. A kind the
// catalogue cannot read is an error, never "runs nothing".
func (e *Environment) serviceNeeds(ctx context.Context, needs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if e.needs == nil {
		return out, nil
	}
	for _, n := range needs {
		k, err := e.needs.ByCapability(ctx, n)
		if errors.Is(err, dependency.ErrUnknown) {
			continue // the boot refuses an unknown need with its own sentence
		}
		if err != nil {
			return nil, fmt.Errorf("reading the need %s from the catalogue: %w", n, err)
		}
		out[n] = k.Local.Image != ""
	}
	return out, nil
}

// appliedOf records what out, the contract a generation runs, applied of
// base: each part's and each service need's effective resources, the
// environment the profile changed, and the overrides.
func appliedOf(out, base *appcontract.Contract, name string, how appcontract.ProfileChoice, overrides map[string]Override, serviceNeeds map[string]bool) AppliedProfile {
	a := AppliedProfile{Profile: name, How: how, Parts: map[string]AppliedPart{}}
	for _, p := range out.Parts {
		ap := AppliedPart{Resources: p.Resources, Public: p.Public}
		if ch, ok := base.Profiles[name][p.Name]; ok && len(ch.Env) > 0 {
			ap.Env = ch.Env
		}
		if o, ok := overrides[p.Name]; ok {
			ap.Override = o.Resources
			ap.MemoryUnlimited = o.MemoryUnlimited
		}
		a.Parts[p.Name] = ap
	}
	for _, n := range out.Needs {
		r, declared := out.NeedResources[n]
		if !declared && !serviceNeeds[n] {
			continue
		}
		ap := AppliedPart{Resources: r, Need: true}
		if size, ok := out.NeedSizes[n]; ok {
			size.Command = slices.Clone(size.Command)
			ap.Size = &size
		}
		if o, ok := overrides[n]; ok {
			ap.Override = o.Resources
			ap.MemoryUnlimited = o.MemoryUnlimited
		}
		a.Parts[n] = ap
	}
	return a
}

// effectiveContract is the contract a release of dep runs: base under
// the deployment's profile, with its operator overrides over that, and
// the record of what it applies. Every release to a standing deployment
// takes it (execute, the intranet release); rebuilds and rehearsals
// reapply a generation's record instead (AppliedProfile.On).
func (s *Service) effectiveContract(ctx context.Context, dep *Deployment, ws string, base *appcontract.Contract) (*appcontract.Contract, AppliedProfile, error) {
	profiled, chosen, err := s.env.Profiled(ctx, dep.ProjectID, dep.Name, ws, base)
	if err != nil {
		return nil, AppliedProfile{}, err
	}
	overrides, err := s.Overrides(ctx, dep.ID)
	if err != nil {
		return nil, AppliedProfile{}, err
	}
	over := map[string]appcontract.Resources{}
	for part, o := range overrides {
		over[part] = o.Resources
	}
	out := profiled.WithResources(over)
	serviceNeeds, err := s.env.serviceNeeds(ctx, out.Needs)
	if err != nil {
		return nil, AppliedProfile{}, err
	}
	applied := appliedOf(out, base, chosen.Profile, chosen.How, overrides, serviceNeeds)
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, AppliedProfile{}, err
	}
	containerLimits := DescribeTarget(target).ContainerMemoryLimits
	if !containerLimits {
		for _, o := range overrides {
			if o.MemoryUnlimited {
				return nil, AppliedProfile{}, fault.New(fault.Invalid, "explicit unlimited memory is unavailable on a provider-managed target")
			}
		}
	}
	if containerLimits {
		return s.env.defaultMemory(ctx, out, applied)
	}
	return out, applied, nil
}

// words is the applied record as one log line: the profile, and each
// part's limits.
func (a AppliedProfile) words() string {
	if !a.Recorded() {
		return "not recorded"
	}
	parts := make([]string, 0, len(a.Parts))
	for name, p := range a.Parts {
		w := fmt.Sprintf("%s: memory %s, cpu %s", name, p.Resources.MemoryWords(), p.Resources.CPUWords())
		if p.Override.Declared() {
			w += " (an operator's override)"
		}
		if p.RunsOn != "" {
			w += ", runs on " + p.RunsOn
		}
		parts = append(parts, w)
	}
	sort.Strings(parts)
	return fmt.Sprintf("profile %s (%s); %s", a.Profile, a.How, strings.Join(parts, "; "))
}

// Fitted is the record with what a rounding target answers each part runs
// on (deploy.ResourceFitter), asked before any effect; a target that holds
// declarations exactly is asked nothing and the record is unchanged.
func (a AppliedProfile) Fitted(ctx context.Context, d any, c *appcontract.Contract) (AppliedProfile, error) {
	fit, ok := d.(deploy.ResourceFitter)
	if !ok {
		return a, nil
	}
	runsOn, err := fit.FitResources(ctx, c)
	if err != nil {
		return a, err
	}
	parts := make(map[string]AppliedPart, len(a.Parts))
	for name, p := range a.Parts {
		p.RunsOn = runsOn[name]
		parts[name] = p
	}
	a.Parts = parts
	return a, nil
}

// AppliedOf is what the generation a release made applied of its
// deployment's profile and overrides: what a rehearsal of that release
// runs. A release that made no generation, or one from before the record
// was kept, has none (not recorded).
func (s *Releases) AppliedOf(ctx context.Context, releaseID ids.ID) (AppliedProfile, error) {
	raw, err := releasesql.New(s.db.SQL()).AppliedByRelease(ctx, releaseID)
	if found, err := database.Lookup(err); err != nil || !found {
		return AppliedProfile{}, err
	}
	return appliedFrom(raw)
}
