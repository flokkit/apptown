package verification

// The memory probes of the runtime evaluator (the deploy/memory-declared
// block). Both are judged from the forge's own records, never a pattern over
// a file:
//
//	memory-declared  the contract, as the forge's parser reads it: each part
//	                 the forge runs from a build or an image declares its
//	                 memory. A static part is served by the forge's own file
//	                 server and declares nothing.
//	memory-within    the standing deployment as it runs: each part the
//	                 release runs from a build or an image uses less than a
//	                 share of the limit its own container is held to, observed
//	                 while the deployment stays the generation it recorded.
//	                 Static parts are the forge's file server's, as the
//	                 release's contract says.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/strs"
)

// judgeMemoryDeclared says whether every part the forge runs from a build
// or an image declares its memory. Only the part's own declaration counts:
// a profile changes it for the deployments that use that profile, and a
// part with none of its own runs unlimited wherever no profile sets one,
// previews included.
func judgeMemoryDeclared(c *appcontract.Contract) check.ProbeVerdict {
	var missing, declared []string
	for i := range c.Parts {
		p := &c.Parts[i]
		if p.IsStatic() {
			continue
		}
		if p.Resources.MemoryBytes <= 0 {
			missing = append(missing, p.Name)
			continue
		}
		declared = append(declared, p.Name+" "+p.Resources.MemoryWords())
	}
	switch {
	case len(missing) > 0:
		return check.ProbeVerdict{Outcome: check.DoesNotConform, Detail: fmt.Sprintf(
			"%s declares no memory; an enforced default is not an app declaration: add  resources: { memory: <size> }  under the part in .forge/app.yaml, measured to fit (deploy/memory-declared says how for each runtime)",
			partList(missing))}
	case len(declared) == 0:
		return check.ProbeVerdict{Outcome: check.Conforms, Detail: "only static parts: AppTown's own file server serves them and they declare nothing"}
	}
	return check.ProbeVerdict{Outcome: check.Conforms, Detail: "declared: " + strings.Join(declared, ", ")}
}

func partList(names []string) string {
	if len(names) == 1 {
		return "part " + names[0]
	}
	return "parts " + strings.Join(names, ", ")
}

// contractJudged is the contract a memory-declared probe judges: the tree a
// gate or a what-if checks, else the commit a review or a baseline names
// (deployed or not). What cannot be read is a reason.
func (o Runtime) contractJudged(ctx context.Context, t Target) (*appcontract.Contract, string) {
	var c *appcontract.Contract
	var err error
	switch {
	case t.ContractDir != "":
		c, err = docker.ReadWorkspaceContract(t.ContractDir)
	case t.Slug != "" && t.Revision != "":
		c, err = docker.ReadContractAt(ctx, o.repo(t.Slug), t.Revision)
	default:
		return nil, "which commit to judge is not known, so the contract cannot be read"
	}
	switch {
	case errors.Is(err, docker.ErrNoContract):
		return nil, "the app has no .forge/app.yaml, so nothing declares what its parts may use"
	case err != nil:
		return nil, "the app's contract could not be read: " + err.Error()
	case c == nil:
		return nil, "the app's contract is not known"
	}
	return c, ""
}

// memoryDeclared judges the contract a target names.
func (o Runtime) memoryDeclared(ctx context.Context, t Target) check.ProbeVerdict {
	c, why := o.contractJudged(ctx, t)
	if why != "" {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: why}
	}
	return judgeMemoryDeclared(c)
}

// memoryWithin judges a standing deployment as it runs: each part's use
// against the limit its own container is held to, sampled while the public
// container stays the generation the deployment recorded, so the release's
// contract (which parts are static sites) is the one that runs. A part with
// no limit is a finding; a deployment mid-change, or what the owner cannot
// observe, is unknown, never within.
func (o Runtime) memoryWithin(ctx context.Context, t Target, share int) check.ProbeVerdict {
	if t.Deployment == nil {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "judged on a standing deployment; there is none here"}
	}
	applied, err := o.deployment.SettledApplied(ctx, t.Deployment)
	if err != nil {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "what the deployment's generation applied could not be read: " + err.Error(), Fault: true}
	}
	if !applied.Recorded() {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "the deployment was settled before AppTown recorded what it applied; deploy it again to judge it"}
	}
	running, err := o.deployment.MemoryOfRecordedGeneration(ctx, t.Deployment)
	if err != nil {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "its memory use could not be read: " + err.Error(), Fault: true}
	}
	if running.Commit == "" || t.Slug == "" {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "not judged against the generation the deployment recorded: " + running.Reason + "; review it again once it has settled"}
	}
	// Which parts are static sites is the running release's contract: the
	// forge's own file server serves those, and they declare nothing.
	release, err := docker.ReadContractAt(ctx, o.repo(t.Slug), running.Commit)
	if err != nil || release == nil {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "the contract of the release it runs could not be read, so which of its parts are static sites is not known"}
	}
	static := map[string]bool{}
	for i := range release.Parts {
		static[release.Parts[i].Name] = release.Parts[i].IsStatic()
	}
	// A part or need the kernel killed at its limit in the last day was
	// over it, whatever it uses now (the Docker host's limit events).
	kills, err := o.deployment.LimitSummary(ctx, t.Deployment, 24*time.Hour)
	if err != nil {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "its limit events could not be read: " + err.Error(), Fault: true}
	}
	// Which needs declare a limit is the generation's applied record: an
	// unreadable sample is never taken for an undeclared need.
	declaredNeeds := map[string]bool{}
	for name, p := range applied.Parts {
		if p.Need && p.Resources.MemoryBytes > 0 {
			declaredNeeds[name] = true
		}
	}
	return judgeMemoryWithin(static, declaredNeeds, running.Parts, killedOutOfMemory(kills), share)
}

// killedOutOfMemory is how often each part or need was killed out of
// memory, from a deployment's limit events.
func killedOutOfMemory(events []deployment.LimitCount) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		if e.Kind == deployment.LimitOutOfMemory {
			out[e.Part] += e.Count
		}
	}
	return out
}

// judgeMemoryWithin is the judgement over what the owner observed: each
// part's use and the limit its container is held to, and how often each
// was killed out of memory. static names the parts the running release
// serves as static sites; a part it does not name is not known to be
// either. A need's service is judged when its generation declared it a
// limit (declaredNeeds), as a part is; an undeclared need is not a finding
// (memory-declared exempts needs).
func judgeMemoryWithin(static, declaredNeeds map[string]bool, used []deployment.PartMemory, killed map[string]int, share int) check.ProbeVerdict {
	var unlimited, over, within, unknown, kills []string
	// Every kill recorded in the window is judged, whatever the parts are
	// called now: one recorded before the first generation settled names
	// the front door's container, not its part.
	for _, name := range slices.Sorted(maps.Keys(killed)) {
		if killed[name] > 0 {
			kills = append(kills, fmt.Sprintf("%s was killed out of memory %d time(s) in the last day", strs.FirstNonBlank(name, "the front door"), killed[name]))
		}
	}
	served := 0
	for _, m := range used {
		name := m.Part
		if m.Need && !declaredNeeds[name] {
			continue // an undeclared need is not judged (memory-declared exempts needs)
		}
		isStatic, named := static[name]
		if !m.Need && named && isStatic {
			continue
		}
		served++
		switch {
		case !m.Need && !named:
			unknown = append(unknown, name+": the release's contract does not name it")
		case !m.Known:
			unknown = append(unknown, name+": "+strs.FirstNonBlank(m.Reason, "not reported"))
		case m.LimitBytes <= 0:
			unlimited = append(unlimited, name)
		case m.UsedBytes*100 >= m.LimitBytes*int64(share):
			over = append(over, fmt.Sprintf("%s uses %s of its %s limit (%d%%)", name, appcontract.FormatMemory(m.UsedBytes), appcontract.FormatMemory(m.LimitBytes), m.UsedBytes*100/m.LimitBytes))
		default:
			within = append(within, fmt.Sprintf("%s %s of %s", name, appcontract.FormatMemory(m.UsedBytes), appcontract.FormatMemory(m.LimitBytes)))
		}
	}
	var findings []string
	if len(kills) > 0 {
		findings = append(findings, strings.Join(kills, "; ")+": it reached its limit; declare more memory or find why it grew")
	}
	if len(unlimited) > 0 {
		findings = append(findings, partList(unlimited)+" runs with no memory limit: nothing holds it to a size; declare resources.memory and deploy again")
	}
	if len(over) > 0 {
		findings = append(findings, strings.Join(over, "; ")+fmt.Sprintf(": at or over %d%% of the limit, so the kernel may stop it; declare more memory or find why it grew", share))
	}
	switch {
	case len(findings) > 0:
		return check.ProbeVerdict{Outcome: check.DoesNotConform, Detail: strings.Join(findings, ". ")}
	case len(unknown) > 0:
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "memory use is not known: " + strings.Join(unknown, "; ")}
	case len(used) > 0 && served == 0:
		return check.ProbeVerdict{Outcome: check.Conforms, Detail: "only static parts: AppTown's own file server serves them, and this check judges nothing of it"}
	case len(within) == 0:
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "the deployment's generation recorded no parts"}
	}
	return check.ProbeVerdict{Outcome: check.Conforms, Detail: fmt.Sprintf("under %d%% of each limit: %s", share, strings.Join(within, ", "))}
}
