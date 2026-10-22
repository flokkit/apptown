package deployment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// SettledApplied is what the generation a deployment settled on applied
// of its profile and overrides: what runs now. Nothing settled, or a
// generation from before the record was kept, is not recorded.
func (s *Service) SettledApplied(ctx context.Context, d *Deployment) (AppliedProfile, error) {
	applied, _, err := s.settled(ctx, d)
	return applied, err
}

// settled is the settled generation's applied record and when the
// deployment settled on it: when that generation's attempt finished. A
// zero time is not known (nothing settled, or an attempt that kept no
// finish time).
func (s *Service) settled(ctx context.Context, d *Deployment) (AppliedProfile, time.Time, error) {
	if d.ObservedGeneration == 0 {
		return AppliedProfile{}, time.Time{}, nil
	}
	at, err := deploymentsql.New(s.db.SQL()).CompletedAttempt(ctx, deploymentsql.CompletedAttemptParams{DeploymentID: d.ID, Generation: d.ObservedGeneration, Status: AttemptDone.String()})
	if found, err := database.Lookup(err); err != nil || !found {
		return AppliedProfile{}, time.Time{}, err
	}
	var since time.Time
	if at.FinishedAt.Valid {
		since = at.FinishedAt.Time
	}
	in, err := releasesql.New(s.db.SQL()).DeploymentInputs(ctx, at.ID)
	if found, err := database.Lookup(err); err != nil || !found {
		return AppliedProfile{}, since, err
	}
	applied, err := appliedFrom(in.Applied)
	return applied, since, err
}

// PartMemory is one part's memory use now, or why it is unknown. A known
// use is one container's, sampled while that container stayed the same, with
// the limit that container is held to (LimitBytes; 0 is no limit).
type PartMemory struct {
	Part       string `json:"part"`
	Need       bool   `json:"need,omitempty"` // a need's service, not an app part
	UsedBytes  int64  `json:"usedBytes"`
	LimitBytes int64  `json:"limitBytes"`
	Known      bool   `json:"known"`
	Reason     string `json:"reason,omitempty"` // why it is unknown
}

// ObservedMemory is each part's memory use now, asked of the deployment's
// own target: a Docker host's daemon reports each part's container. A
// target that does not report it (App Platform, Portainer), a daemon that
// cannot be asked, or a part it does not report is unknown with the
// reason: never zero, and never "within its limit". The parts are those
// the settled generation recorded, else the public part alone.
func (s *Service) ObservedMemory(ctx context.Context, d *Deployment) ([]PartMemory, error) {
	applied, err := s.SettledApplied(ctx, d)
	if err != nil {
		return nil, err
	}
	var parts []string
	for name := range applied.Services {
		parts = append(parts, name)
	}
	for name := range applied.Parts {
		parts = append(parts, name)
	}
	sort.Strings(parts)
	unknown := func(reason string) []PartMemory {
		if len(parts) == 0 {
			return []PartMemory{{Reason: reason}}
		}
		out := make([]PartMemory, len(parts))
		for i, p := range parts {
			out[i] = PartMemory{Part: p, Need: applied.Parts[p].Need, Reason: reason}
		}
		return out
	}
	if d.Container == "" {
		return unknown("its target does not report memory use to AppTown"), nil
	}
	dk, err := s.hosts.DockerScopeFor(ctx, d.TargetID)
	if errors.Is(err, ErrNoLocalDocker) || err != nil {
		return unknown(fmt.Sprintf("its host cannot be asked: %v", err)), nil
	}
	proj, err := s.projects.ByID(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}
	containerOf := partContainers(d, proj.Slug, applied)
	if len(parts) == 0 {
		parts = []string{""}
	}
	containers := make([]string, 0, len(containerOf))
	for _, c := range containerOf {
		containers = append(containers, c)
	}
	sort.Strings(containers)
	// Each sample is bound to one container: inspected before and after,
	// the same container both times, its limit read from it. A container
	// replaced meanwhile (a deploy, an override) answers for nothing.
	before := map[string]docker.Runtime{}
	for _, c := range containers {
		if rt, err := docker.Inspect(ctx, dk, c); err == nil {
			before[c] = rt
		}
	}
	used, err := docker.MemoryUsage(ctx, dk, containers)
	if err != nil {
		return unknown(err.Error()), nil
	}
	out := make([]PartMemory, 0, len(parts))
	for _, p := range parts {
		c := containerOf[p]
		b, reported := used[c]
		first, inspected := before[c]
		after, err := docker.Inspect(ctx, dk, c)
		switch {
		case !reported:
			out = append(out, PartMemory{Part: p, Reason: "the daemon did not report its container"})
		case !inspected || err != nil:
			out = append(out, PartMemory{Part: p, Reason: "its container could not be inspected, so the limit it is held to is not known"})
		case first.ID != after.ID:
			out = append(out, PartMemory{Part: p, Reason: "its container was replaced while it was being observed"})
		default:
			out = append(out, PartMemory{Part: p, UsedBytes: b, LimitBytes: first.MemoryLimit, Known: true})
		}
		out[len(out)-1].Need = applied.Parts[p].Need
	}
	return out, nil
}

// GenerationMemory is a deployment's memory as its recorded generation runs
// it: the commit that generation serves and each part's use, observed while
// its public container stayed that generation. Commit is "" with the reason
// when that cannot be established: the deployment is changing, or its
// container cannot be asked.
type GenerationMemory struct {
	Commit string
	Parts  []PartMemory
	Reason string
}

// MemoryOfRecordedGeneration observes each part's memory only while the
// deployment is settled on the generation it recorded: its public container
// is that generation before and after the sample, and after it, read again,
// no attempt is open and no generation was admitted that it has not reached
// (a rollout starts new parts before it replaces the public container, and a
// failed one can leave them running). Otherwise it answers for nothing, with
// the reason.
func (s *Service) MemoryOfRecordedGeneration(ctx context.Context, d *Deployment) (GenerationMemory, error) {
	served := s.ServedRevision(ctx, d)
	if served.Commit == "" {
		return GenerationMemory{Reason: served.Reason}, nil
	}
	parts, err := s.ObservedMemory(ctx, d)
	if err != nil {
		return GenerationMemory{}, err
	}
	if why, err := s.unsettledReason(ctx, d); err != nil || why != "" {
		return GenerationMemory{Reason: why}, err
	}
	if again := s.ServedRevision(ctx, d); again.Commit != served.Commit || again.ContainerID != served.ContainerID {
		return GenerationMemory{Reason: "the deployment changed while it was being observed"}, nil
	}
	return GenerationMemory{Commit: served.Commit, Parts: parts}, nil
}

// unsettledReason says why a deployment, read again now, is not settled on
// the generation d recorded, or "" when it is.
func (s *Service) unsettledReason(ctx context.Context, d *Deployment) (string, error) {
	now, err := s.Deployments.ByID(ctx, d.ID)
	if err != nil {
		return "", err
	}
	open, err := deploymentsql.New(s.db.SQL()).HasOpenAttempt(ctx, deploymentsql.HasOpenAttemptParams{DeploymentID: d.ID, RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
	if err != nil {
		return "", err
	}
	switch {
	case open:
		return "a deploy of it is in progress", nil
	case now.DesiredGeneration != now.ObservedGeneration:
		return fmt.Sprintf("it has not settled on generation %d, which was admitted after the one it runs", now.DesiredGeneration), nil
	case now.ObservedGeneration != d.ObservedGeneration:
		return "it settled on another generation since it was read", nil
	}
	return "", nil
}

// DeploymentResources is what a deployment runs with now, part by part:
// the profile its settled generation applied and how it was chosen, the
// profile chosen for its next deploy, each part's applied limits, its
// operator's override, and its memory use now.
type DeploymentResources struct {
	Recorded bool                      `json:"recorded"` // false: the settled generation kept no record
	Profile  string                    `json:"profile,omitempty"`
	How      appcontract.ProfileChoice `json:"how,omitempty"`
	// Chosen is the profile explicitly chosen for the deployment's next
	// deploy, "" when none is.
	Chosen string         `json:"chosen,omitempty"`
	Parts  []PartResource `json:"parts"`
	// Share is what the deployment holds of its target: its limits and
	// use against a fixed host's total, or that the target is elastic.
	Share *HostShare `json:"share,omitempty"`
	// Limits counts its limit events over the last day, per part and
	// kind; LimitsSource says where they are read from, or why none can
	// be (an empty list is "none recorded" only when LimitsKnown).
	Limits       []LimitCount `json:"limits"`
	LimitsKnown  bool         `json:"limitsKnown"`
	LimitsSource string       `json:"limitsSource"`
}

// PartResource is one part's or need's applied limits, override and use.
type PartResource struct {
	Size string `json:"size,omitempty"`
	// SuppliedService is resized through the workflow declaration, not overrides.
	SuppliedService bool                 `json:"suppliedService,omitempty"`
	MemoryDefault   *MemoryDefaultSource `json:"memoryDefault,omitempty"`
	MemoryUnlimited bool                 `json:"memoryUnlimited,omitempty"`
	Part            string               `json:"part"`
	Public          bool                 `json:"public"`
	Need            bool                 `json:"need,omitempty"` // a need's service, not an app part
	Memory          string               `json:"memory"`         // "512Mi" or "no limit"
	CPU             string               `json:"cpu"`
	RunsOn          string               `json:"runsOn,omitempty"`
	Override        *Override            `json:"override,omitempty"`
	OverrideApplied bool                 `json:"overrideApplied"` // the settled generation carries the override
	Observed        PartMemory           `json:"observed"`
}

// Resources reads what a deployment runs with now; the caller needs the
// deployment read grant on its project.
func (s *Service) Resources(ctx context.Context, projectID ids.ID, name string) (DeploymentResources, error) {
	if s.authority == nil {
		return DeploymentResources{}, access.ErrAuthorityUnavailable
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.resources"}}); err != nil {
		return DeploymentResources{}, err
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return DeploymentResources{}, err
	}
	applied, err := s.SettledApplied(ctx, dep)
	if err != nil {
		return DeploymentResources{}, err
	}
	chosen, err := s.env.secrets.Profiles.Chosen(ctx, projectID, dep.Name)
	if err != nil {
		return DeploymentResources{}, fmt.Errorf("reading the profile chosen for %s: %w", dep.Name, err)
	}
	overrides, err := s.Overrides(ctx, dep.ID)
	if err != nil {
		return DeploymentResources{}, err
	}
	observed, err := s.ObservedMemory(ctx, dep)
	if err != nil {
		return DeploymentResources{}, err
	}
	use := map[string]PartMemory{}
	for _, m := range observed {
		use[m.Part] = m
	}
	out := DeploymentResources{Recorded: applied.Recorded(), Profile: applied.Profile, How: applied.How, Chosen: chosen, Parts: []PartResource{}}
	names := map[string]bool{}
	for n := range applied.Services {
		names[n] = true
	}
	for n := range applied.Parts {
		names[n] = true
	}
	for n := range overrides {
		names[n] = true
	}
	for _, n := range slices.Sorted(maps.Keys(names)) {
		p, ok := applied.Parts[n]
		_, supplied := applied.Services[n]
		if !ok {
			p = applied.Services[n]
		}
		pr := PartResource{SuppliedService: supplied, MemoryDefault: p.MemoryDefault, MemoryUnlimited: p.MemoryUnlimited, Part: n, Public: p.Public, Need: p.Need, Memory: p.Resources.MemoryWords(), CPU: p.Resources.CPUWords(), RunsOn: p.RunsOn}
		if p.Size != nil {
			pr.Size = p.Size.Name
		}
		switch {
		case !applied.Recorded():
			pr.Memory, pr.CPU = "not recorded", "not recorded"
		case p.RunsOn != "":
			// A target that rounds to its own sizes: what was declared, and
			// RunsOn says the size it runs on and that size's capacity.
			// Nothing there is unlimited.
			pr.Memory, pr.CPU = declaredWords(p.Resources.MemoryBytes > 0, p.Resources.MemoryWords()), declaredWords(p.Resources.CPUs > 0, p.Resources.CPUWords())
		}
		if o, ok := overrides[n]; ok {
			pr.Override = &o
			pr.OverrideApplied = applied.Recorded() && p.Override == o.Resources && p.MemoryUnlimited == o.MemoryUnlimited
		}
		if m, ok := use[n]; ok {
			pr.Observed = m
		} else {
			pr.Observed = PartMemory{Part: n, Reason: "not asked"}
		}
		out.Parts = append(out.Parts, pr)
	}
	if len(out.Parts) == 0 && len(observed) == 1 {
		out.Parts = append(out.Parts, PartResource{Memory: "not recorded", CPU: "not recorded", Public: true, Observed: observed[0]})
	}
	out.Share = s.share(ctx, dep, out.Parts, applied)
	out.LimitsKnown, out.LimitsSource = s.LimitEventsSource(ctx, dep)
	if out.Limits, err = s.LimitSummary(ctx, dep, 24*time.Hour); err != nil {
		return DeploymentResources{}, err
	}
	return out, nil
}

// declaredWords is a declaration as a target that rounds it shows it:
// "768Mi declared", or "not declared" (the size it runs on decides).
func declaredWords(declared bool, words string) string {
	if !declared {
		return "not declared"
	}
	return words + " declared"
}

// partContainers is the container each part and need of a deployment's
// settled generation runs in: the public part in the deployment's own
// recorded container, any other part and each need as a service on its
// stack (bringUpParts, the needs' services). A generation that recorded no
// parts is known by its public container alone, under the name "".
func partContainers(d *Deployment, slug string, applied AppliedProfile) map[string]string {
	if len(applied.Parts) == 0 {
		return map[string]string{"": d.Container}
	}
	out := map[string]string{}
	// A prefix the record does not know names nothing: no container is
	// guessed under another one.
	names, err := d.HostNames()
	if err != nil {
		return out
	}
	for n := range applied.Services {
		out[n] = names.Service(docker.StackID(slug, d.Placement().Name), n)
	}
	for p, ap := range applied.Parts {
		if ap.Public {
			out[p] = d.Container
		} else {
			out[p] = names.Service(docker.StackID(slug, d.Placement().Name), p)
		}
	}
	return out
}
