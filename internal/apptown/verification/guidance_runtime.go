package verification

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/strs"
)

// runRuntimeChecksForRun probes each runtime check against the change's
// live preview. No preview (a run with no change, or the preview not up)
// is cannot-assess with the reason, never a silent pass — and for a
// remediation whose target this is, that reads as unmet until the boot
// step has run. unjudged names each check at or above failOn that could not
// be judged although it should have been: probed, or its run already booted
// the preview that could not be found. The step holds on those rather than
// pass.
//
// A pinned landing (candidate set) probes only a preview AppTown recorded
// booting from the candidate itself (a change being landed is previewed as
// its candidate): any other may serve an earlier source or later edits. With
// none, the landing boots the change's preview from the candidate itself and
// probes it once that proof holds; a failed boot leaves its runtime checks not
// judged, with the boot's error, and those at or above failOn hold the step.
// A candidate with no runtime contract has nothing to boot or hold.
func (a *Guidance) runRuntimeChecksForRun(ctx context.Context, run *workflow.Run, checks []conformance.DeclaredCheck, contractDir, commit, failOn, candidate string) (results Results, findings []conformance.Finding, unjudged []string) {
	var target *guidanceTarget
	var served *preview.Served
	var why string
	// landing: a pinned landing that needs its candidate's preview, which
	// holds the step on any required check it could not judge.
	landing := false
	resolved := false
	resolve := func() {
		if resolved {
			return
		}
		resolved = true
		if candidate != "" {
			commit = candidate
		}
		if run.ChangeID.IsZero() {
			why = "runtime checks need a change's preview; this run has no change"
			return
		}
		// A pinned landing needs its candidate's app judged unless the
		// candidate declares no runtime contract (the boot step's vacuous
		// pass): decided from the checked tree first, so any failure below,
		// a preview that could not be asked about included, holds the step.
		if candidate != "" {
			if contractDir != "" {
				if _, err := docker.ReadSelectedContract(contractDir); errors.Is(err, docker.ErrNoContract) {
					why = "the landing's candidate " + strs.ShortSHA(candidate) + " declares no runtime contract (.apptown/app.yaml), so there is no app to probe"
					return
				}
			}
			landing = true
		}
		ch, err := workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
		if err != nil {
			why = "runtime checks need a change's preview; the change could not be read"
			return
		}
		// The lane's boot step leaves the RUN's preview (apptown-preview-run-N);
		// the try-it button leaves the CHANGE's (apptown-preview-change-N).
		// Either is probed only when it is the checked commit, booted by this
		// AppTown, which recorded what it was booted with: the check judges
		// that record, never today's settings or another tree. Prefer the
		// run's.
		other := false
		open := func(labels ...string) bool {
			for _, label := range labels {
				p, ok, err := a.previews.OpenPreview(ctx, run.ProjectID, label)
				if err != nil {
					why = "the change's preview could not be asked about: " + err.Error()
					return false
				}
				if !ok {
					continue
				}
				if commit == "" || p.Source != commit || p.Recorded == nil {
					p.Close()
					other = true
					continue
				}
				served = p
				break
			}
			return true
		}
		changeLabel := fmt.Sprintf("change-%d", ch.ID)
		if !open(fmt.Sprintf("run-%d", run.ID), changeLabel) {
			return
		}
		// A landing has no boot step: it boots the change's preview from
		// its candidate itself, as a person's request would, and probes it
		// only once the same proof holds of what then runs.
		if served == nil && candidate != "" {
			if _, err := a.previews.RehearseChange(ctx, preview.Change{ID: ch.ID, ProjectID: run.ProjectID, Pinned: candidate}, false); err != nil {
				why = "AppTown booted the change's preview from the landing's candidate " + strs.ShortSHA(candidate) + " for its runtime checks, and the boot failed: " + err.Error()
				return
			}
			if !open(changeLabel) {
				return
			}
		}
		if served == nil {
			switch {
			case candidate != "":
				why = "AppTown booted the change's preview from the landing's candidate " + strs.ShortSHA(candidate) + ", and the preview it then found is not proven to run it; run this step again"
			case other:
				why = "the running preview is not the checked commit " + strs.ShortSHA(commit) + " as this AppTown booted it; boot it again to probe it"
			default:
				why = "the change's preview is not running — the boot step provides it; nothing to probe yet"
			}
			return
		}
		slugMeta, slugErr := project.ByIDIn(ctx, a.db, run.ProjectID)
		slug := slugMeta.Slug
		if _, err := database.Lookup(slugErr); err != nil {
			why = "runtime checks need the project's name to mint its preview address; it could not be read"
			return
		}
		target = &guidanceTarget{ProjectID: run.ProjectID, Slug: slug, Container: served.Name,
			FrontDoor: served.FrontDoor, Naked: served.Naked, ContractDir: contractDir, Served: served, Recorded: served.Recorded}
	}
	defer func() { served.Close() }()
	booted := !run.ChangeID.IsZero() && a.previewBooted(ctx, run)
	for _, sc := range checks {
		if !check.IsRuntime(sc.Check) {
			continue
		}
		start := a.now()
		ps, perr := check.ParseProbe(sc.Check.Probe)
		if perr == nil && ps.Standing() {
			// Answered only by a standing deployment's steady use, which a
			// preview booted for this change does not have: judged in
			// reviews, and never a reason to hold a landing.
			ev := check.Evaluation{Declared: sc.Declared(), Mode: check.Gate, Outcome: check.CannotAssess}
			res := resultOf(ev)
			res.Verdict = workflow.VerdictSkipped
			res.Detail = "judged on the standing deployment in reviews, not on a preview"
			res.DurationMs = new(a.now().Sub(start).Milliseconds())
			results = append(results, res)
			continue
		}
		contractOnly := perr == nil && !ps.NeedsApp()
		if !contractOnly {
			resolve()
		}
		// No preview to probe is AppTown's fault, never the implementer's:
		// the change must not be sent to rework over it.
		ev := check.Evaluation{Declared: sc.Declared(), Mode: check.Gate,
			Outcome: check.CannotAssess, Detail: why, Fault: true}
		if contractOnly {
			// The checked tree answers it; no preview is needed. Without the
			// tree it is unknown, never judged on another commit.
			ev.Detail = "the checked tree is not available"
			if contractDir != "" {
				v := a.runtime.Probe(ctx, guidanceTarget{ProjectID: run.ProjectID, ContractDir: contractDir}, sc.Check.Probe)
				ev.Outcome, ev.Detail, ev.Fault = v.Outcome, v.Detail, v.Fault
			}
		} else if target != nil {
			v := a.runtime.Probe(ctx, *target, sc.Check.Probe)
			ev.Outcome, ev.Detail, ev.Fault = v.Outcome, v.Detail, v.Fault
			// The answer is about the container that was opened, only while
			// it is still that one.
			if same, err := served.Same(ctx); !same {
				detail := "the preview was replaced or stopped while it was being checked; check it again"
				if err != nil {
					detail = "the preview could not be asked about after its probe: " + err.Error()
				}
				ev.Outcome, ev.Detail, ev.Fault = check.CannotAssess, detail, true
			}
		}
		if ev.Outcome == check.DoesNotConform {
			findings = append(findings, conformance.Finding{Finding: check.Finding{
				Fingerprint: conformance.Fingerprint(sc, ""),
				Kind:        "conformance",
				Severity:    sc.Check.Severity,
				Title:       fmt.Sprintf("guidance %s v%d · %s", sc.Block, sc.Version, sc.Check.Name),
				Detail:      strs.FirstNonBlank(ev.Detail, sc.Check.Expects),
				Source:      "guidance",
			}})
		}
		if (target != nil || booted || (landing && !contractOnly)) && ev.Outcome != check.Conforms && ev.Outcome != check.DoesNotConform && check.SeverityRank(sc.Check.Severity) >= check.SeverityRank(failOn) {
			unjudged = append(unjudged, sc.Block+"|"+sc.Check.Name+": "+ev.Detail)
		}
		res := resultOf(ev)
		res.DurationMs = new(a.now().Sub(start).Milliseconds())
		res.Detail = strs.Truncate(ev.Detail, 600)
		if res.Verdict != workflow.VerdictPass && res.Verdict != workflow.VerdictFail {
			// Whatever the probe could not answer reads one way on the
			// record: AppTown could not assess it.
			res.Verdict = workflow.VerdictCannotAssess
		}
		results = append(results, res)
	}
	return results, findings, unjudged
}

// previewBooted says whether this run has already started its preview: a
// preview step of the run passed. Such a run requires that preview for its
// runtime checks, so not finding it now (stopped, no port, a failed read) is
// AppTown's to answer. A run that has not booted one, because its workflow
// has none (the landing) or boots it in a later stage, records its runtime
// checks unjudged. Witnessed from the run's steps, not inferred from its
// workflow's shape: inferred, a preview in a later stage would make an
// earlier check wait for it. A failed read holds.
func (a *Guidance) previewBooted(ctx context.Context, run *workflow.Run) bool {
	steps, err := workflow.ReadScopedStepsIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return true
	}
	for _, st := range steps {
		if st.Type == "preview" && st.Status == workflow.StatusPass {
			return true
		}
	}
	return false
}

// runtimeBaseline judges on main the runtime checks the change's preview
// failed, so a problem main already had is not labelled the change's. Main's
// deployment is the base only when it serves exactly the change's base
// commit, as the deployment owner witnesses it; otherwise each such check is
// recorded cannot-assess with the reason, and its finding reads undecided:
// shown, not blocking.
func (a *Guidance) runtimeBaseline(ctx context.Context, projectID ids.ID, slug string, checks []conformance.DeclaredCheck, findings []conformance.Finding, base string) []check.Evaluation {
	failing := failingChecks(checks, findings, check.IsRuntime)
	if len(failing) == 0 || base == "" {
		return nil
	}
	// A probe of the contract is answered by the base commit itself; only a
	// probe of the running app needs main's deployment to serve it.
	var out []check.Evaluation
	var running []conformance.DeclaredCheck
	for _, sc := range failing {
		if ps, err := check.ParseProbe(sc.Check.Probe); err != nil || ps.NeedsApp() {
			running = append(running, sc)
			continue
		}
		ev := check.Evaluation{Declared: sc.Declared(), Mode: check.Gate}
		v := a.runtime.Probe(ctx, guidanceTarget{ProjectID: projectID, Slug: slug, Revision: base}, sc.Check.Probe)
		ev.Outcome, ev.Detail = v.Outcome, v.Detail
		if v.Outcome == check.DoesNotConform {
			ev.Findings = []check.Finding{{Fingerprint: conformance.Fingerprint(sc, "")}}
		}
		out = append(out, ev)
	}
	if len(running) == 0 {
		return out
	}
	return append(out, probeMainAtBase(ctx, running, base, func() (guidanceTarget, string, func() bool, string) { return a.runtime.mainAt(ctx, projectID) }, a.runtime.Probe)...)
}

// probeMainAtBase probes each check against main's deployment when it
// serves base, re-asking after each probe that the same container still
// serves it. at resolves main's deployment and the commit it serves.
func probeMainAtBase(ctx context.Context, failing []conformance.DeclaredCheck, base string, at func() (guidanceTarget, string, func() bool, string), probe func(context.Context, guidanceTarget, string) check.ProbeVerdict) []check.Evaluation {
	target, commit, still, why := at()
	if why == "" && commit != base {
		why = fmt.Sprintf("main's deployment runs %s, not this change's base %s", strs.ShortSHA(commit), strs.ShortSHA(base))
	}
	out := make([]check.Evaluation, 0, len(failing))
	for _, sc := range failing {
		ev := check.Evaluation{Declared: sc.Declared(), Mode: check.Gate, Outcome: check.CannotAssess, Detail: why}
		if why == "" {
			v := probe(ctx, target, sc.Check.Probe)
			switch {
			case !still():
				ev.Detail = "main's deployment changed while it was being probed"
			case v.Outcome == check.Conforms:
				ev.Outcome, ev.Detail = v.Outcome, v.Detail
			case v.Outcome == check.DoesNotConform:
				ev.Outcome, ev.Detail = v.Outcome, v.Detail
				ev.Findings = []check.Finding{{Fingerprint: conformance.Fingerprint(sc, "")}}
			default:
				ev.Detail = "main's deployment could not be probed: " + v.Detail
			}
		}
		out = append(out, ev)
	}
	return out
}

// failingChecks are the checks of a kind that reported a finding.
func failingChecks(checks []conformance.DeclaredCheck, findings []conformance.Finding, kind func(check.Declaration) bool) []conformance.DeclaredCheck {
	found := map[string]bool{}
	for _, f := range findings {
		found[f.Fingerprint] = true
	}
	var out []conformance.DeclaredCheck
	for _, sc := range checks {
		if kind(sc.Check) && found[conformance.Fingerprint(sc, "")] {
			out = append(out, sc)
		}
	}
	return out
}
