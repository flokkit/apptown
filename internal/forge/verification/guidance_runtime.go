package verification

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
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
// A pinned landing (candidate set) probes only a preview the forge recorded
// booting from the candidate itself (a change being landed is previewed as
// its candidate): any other may serve an earlier source or later edits. With
// none, its runtime checks are not judged; the rules above decide whether
// that holds the step.
func (a *Guidance) runRuntimeChecksForRun(ctx context.Context, run *workflow.Run, checks []conformance.DeclaredCheck, contractDir, commit, failOn, candidate string) (results Results, findings []conformance.Finding, unjudged []string) {
	var target *Target
	var served *preview.Served
	var why string
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
		ch, err := workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
		if err != nil {
			why = "runtime checks need a change's preview; the change could not be read"
			return
		}
		// The lane's boot step leaves the RUN's preview (forge-preview-run-N);
		// the try-it button leaves the CHANGE's (forge-preview-change-N).
		// Either is probed only when it is the checked commit, booted by this
		// forge, which recorded what it was booted with: the check judges
		// that record, never today's settings or another tree. Prefer the
		// run's.
		other := false
		for _, label := range []string{fmt.Sprintf("run-%d", run.ID), fmt.Sprintf("change-%d", ch.ID)} {
			p, ok, err := a.previews.OpenPreview(ctx, run.ProjectID, label)
			if err != nil {
				why = "the change's preview could not be asked about: " + err.Error()
				return
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
		if served == nil {
			switch {
			case candidate != "":
				why = "no preview is proven to run the landing's candidate " + strs.ShortSHA(candidate) + "; boot the change's preview (forge preview <change>, or restart on the change page: a request to boot it boots the candidate while the change is landing, opening a running one does not), then run this step again"
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
		target = &Target{ProjectID: run.ProjectID, Slug: slug, Container: served.Name,
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
		// No preview to probe is the forge's fault, never the implementer's:
		// the change must not be sent to rework over it.
		ev := check.Evaluation{Declared: sc.Declared(), Mode: check.Gate,
			Outcome: check.CannotAssess, Detail: why, Fault: true}
		if contractOnly {
			// The checked tree answers it; no preview is needed. Without the
			// tree it is unknown, never judged on another commit.
			ev.Detail = "the checked tree is not available"
			if contractDir != "" {
				v := a.runtime.Probe(ctx, Target{ProjectID: run.ProjectID, ContractDir: contractDir}, sc.Check.Probe)
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
		if (target != nil || booted) && ev.Outcome != check.Conforms && ev.Outcome != check.DoesNotConform && check.SeverityRank(sc.Check.Severity) >= check.SeverityRank(failOn) {
			unjudged = append(unjudged, sc.Block+"|"+sc.Check.Name+": "+ev.Detail)
		}
		res := resultOf(ev)
		res.DurationMs = new(a.now().Sub(start).Milliseconds())
		res.Detail = strs.Truncate(ev.Detail, 600)
		if res.Verdict != workflow.VerdictPass && res.Verdict != workflow.VerdictFail {
			// Whatever the probe could not answer reads one way on the
			// record: the forge could not assess it.
			res.Verdict = workflow.VerdictCannotAssess
		}
		results = append(results, res)
	}
	return results, findings, unjudged
}

// previewBooted says whether this run has already started its preview: a
// preview step of the run passed. Such a run requires that preview for its
// runtime checks, so not finding it now (stopped, no port, a failed read) is
// the forge's to answer. A run that has not booted one, because its workflow
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
		v := a.runtime.Probe(ctx, Target{ProjectID: projectID, Slug: slug, Revision: base}, sc.Check.Probe)
		ev.Outcome, ev.Detail = v.Outcome, v.Detail
		if v.Outcome == check.DoesNotConform {
			ev.Findings = []check.Finding{{Fingerprint: conformance.Fingerprint(sc, "")}}
		}
		out = append(out, ev)
	}
	if len(running) == 0 {
		return out
	}
	return append(out, probeMainAtBase(ctx, running, base, func() (Target, string, func() bool, string) { return a.runtime.mainAt(ctx, projectID) }, a.runtime.Probe)...)
}

// probeMainAtBase probes each check against main's deployment when it
// serves base, re-asking after each probe that the same container still
// serves it. at resolves main's deployment and the commit it serves.
func probeMainAtBase(ctx context.Context, failing []conformance.DeclaredCheck, base string, at func() (Target, string, func() bool, string), probe func(context.Context, Target, string) check.ProbeVerdict) []check.Evaluation {
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
