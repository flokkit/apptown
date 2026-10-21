package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// Guidance checks (design/subsystems/guidance-checks.md): a guidance block may carry
// CHECKS — verification that its prose was heeded — versioned with the
// prose so the two cannot drift. A guidance/conformance step collects every
// check from the project's imported blocks at their pinned versions and
// runs them against the run's committed tree.
//
// Evaluator kinds:
//
//	sh  — deterministic script in a pinned busybox-class image, read-only
//	      workspace, no network. Exit 0 conformant, 1 findings, else the
//	      check errors (recorded, never silently dropped).
//	llm — a rubric. ALL rubrics compile into ONE fast-slot checklist call
//	      returning per-item verdicts with cited evidence. CALIBRATION
//	      POSTURE: llm findings never gate — findings only, until their
//	      false-positive rate is measured.
//
// Failures are ordinary findings (fingerprint guidance|block@vN|check|file)
// written as a conformance-report artifact; exceptions are the existing
// per-fingerprint dispositions. Only sh findings at/above failOn (default
// high) fail the step. Checks validate, never fix.

// conformanceImage is the pinned image every sh check runs in; the library
// owns it, because it is part of what an sh verdict means.
const conformanceImage = check.ShImage

// RunGuidanceConformance executes a guidance/conformance step.
func (a *Guidance) RunGuidanceConformance(task *workflow.Execution) {
	step := task.Step()
	stepID := step.ID
	run := task.Run()
	var cfg struct {
		FailOn string `json:"failOn"` // sh, tool and runtime findings at/above this severity fail the step; default high
	}
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)
	if cfg.FailOn == "" {
		cfg.FailOn = "high"
	}

	// facts are what other owners judge (workflow.CheckEvidence); evidence
	// maps are this runner's own keys. Once judged, the facts ride on every
	// outcome.
	var facts workflow.CheckEvidence
	finish := func(status workflow.StepState, evidence map[string]any) {
		task.Settle(facts.Outcome(status, evidence))
	}
	if _, err := task.Claim(); err != nil {
		return
	}
	if cfgErr != nil {
		finish(workflow.StatusError, map[string]any{"reason": cfgErr.Error()})
		return
	}
	// A landing checks its pinned candidate, and every outcome names it.
	rev, pinned, err := checkedRevision(task.Context(), a.db, run.ID)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	rework := func(evidence map[string]any) { task.Settle(facts.Rework(evidence)) }
	if pinned {
		report, send := finish, rework
		finish = func(status workflow.StepState, evidence map[string]any) {
			evidence["candidate"] = rev
			report(status, evidence)
		}
		rework = func(evidence map[string]any) {
			evidence["candidate"] = rev
			send(evidence)
		}
	}

	checks, adoption, err := a.collectGuidanceChecksFor(task.Context(), run)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if len(checks) == 0 {
		finish(workflow.StatusPass, map[string]any{"note": "no imported guidance declares checks — nothing to verify"})
		return
	}

	// Same posture as scans: verify the COMMITTED tree, not AppTown runtime
	// state in the working directory.
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	checkedAt, cleanup, err := checkedSource(task.Context(), a.db, a.dataDir, run, source)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	defer cleanup()
	src, commit := checkedAt.Src, checkedAt.Commit

	evidence := map[string]any{"image": conformanceImage, "failOn": cfg.FailOn, "commit": commit}
	if adoption != nil {
		evidence["adoption"] = map[string]any{"revision": adoption.Revision, "digest": adoption.Digest}
	}
	said, err := a.judgeDeclared(task.Context(), task.RunScope(), run, source, checks, checkedAt, cfg.FailOn, fmt.Sprintf("forge-conform-%d", stepID))
	results, findings, shErrors, unjudged := said.results, said.findings, said.errors, said.unjudged
	if said.llm != nil {
		evidence["llm"] = said.llm
	}
	if err != nil {
		facts.Checks = results
		evidence["reason"] = err.Error()
		finish(workflow.StatusError, evidence)
		return
	}

	// The baseline: what already failed on the tree this change forked
	// from is the estate's, not the change's (guidance_baseline.go).
	var ch *workflow.Change
	if !source.ChangeID.IsZero() {
		ch, err = workflow.ReadChangeIn(task.Context(), a.db, source.ChangeID)
		if err != nil {
			finish(workflow.StatusError, map[string]any{"reason": err.Error()})
			return
		}
	}
	baseSHA := workflowBaseline(task.Context(), a.dataDir, source)
	base := a.verification.baselineFor(conformance.WithRun(task.Context(), task.RunScope()), run.ProjectID, checks, baseSHA, fmt.Sprintf("forge-conform-%d", stepID))
	// Model rubrics and runtime probes are not in the cached deterministic
	// baseline: a failing one is judged on main here — the rubric on the
	// base tree, the probe on main's deployment when it serves the base.
	var judged []check.Evaluation
	if base != nil {
		judged = append(a.llmBaseline(task.Context(), source, checks, findings, baseSHA), a.runtimeBaseline(task.Context(), run.ProjectID, source.Slug, checks, findings, baseSHA)...)
	}
	own, already, undecided := classifyAgainstBase(results, findings, checks, base, workflow.AcceptanceScopeOf(ch), judged...)
	if base != nil {
		evidence["baseline"] = map[string]any{"sha": base.sha, "onMain": base.onMain, "isMain": base.isMain, "unknown": len(base.unknown), "note": base.note}
	}
	titlesOf := func(fs []conformance.Finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Title+": "+f.Detail)
		}
		return out
	}
	if len(already) > 0 {
		evidence["preexisting"] = titlesOf(already)
	}
	if len(undecided) > 0 {
		// Handed to the reviewer (workflowexec reads it), never to the
		// implementing agent as work.
		facts.Undecided = titlesOf(undecided)
	}
	// Findings → conformance-report artifact (native format) so the run's
	// findings view and dispositions see them like any scanner's.
	if len(findings) > 0 {
		body, _ := json.MarshalIndent(findings, "", " ")
		if res, err := workflow.AddArtifactIn(context.WithoutCancel(task.Context()), a.db, step.RunID, "conformance-report", "guidance", "", string(body), a.now()); err == nil {
			id := res
			evidence["reportArtifactId"] = id
			workflow.RecordNoticeOrLog(context.WithoutCancel(task.Context()), a.db, step.RunID, "artifact.added", map[string]any{"kind": "conformance-report", "name": "guidance", "id": id}, a.now())
		}
	}

	facts.Checks = results

	dispositions, err := a.conformance.Dispositions(conformance.WithRun(task.Context(), task.RunScope()), run.ProjectID, true)
	if err != nil {
		evidence["reason"] = fmt.Sprintf("reading exceptions: %v", err)
		finish(workflow.StatusError, evidence)
		return
	}
	actionable := conformance.AnnotateFindings(own, dispositions)
	failing := 0
	var failingTitles []string
	for _, f := range actionable {
		// Calibration posture: llm findings NEVER gate. Deterministic evaluators
		// (sh, tool, and a runtime probe of the running preview) gate at
		// failOn. A runtime check AppTown could not run records no finding,
		// so an unreachable preview never gates. Runtime gates too: otherwise
		// a static app's browser wiring check can run, fail and still let
		// adoption pass. (Finding source is uniform;
		// evaluator is in the fingerprint's check — recover it from results.)
		if ev := findingEvaluator(results, f); (ev == "sh" || ev == "tool" || ev == "runtime") && check.SeverityRank(f.Severity) >= check.SeverityRank(cfg.FailOn) {
			failing++
			failingTitles = append(failingTitles, f.Title+": "+f.Detail)
		}
	}
	// "findingsSummary" for counts — "findings" is reserved for the []string
	// shape ${{feedback.latest}}'s parser reads (the reviewer's shape).
	evidence["findingsSummary"] = map[string]any{
		"total": len(findings), "preexisting": len(already), "undecided": len(undecided), "dispositioned": len(own) - len(actionable),
		"actionable": len(actionable), "failing": failing,
	}
	if shErrors > 0 {
		facts.CheckErrors = shErrors
	}
	// Acceptance (§2, §5.2): a remediation's target and an uplift's checks
	// must PASS — not merely be absent from the findings. An error, a skip
	// or an exception on one of them fails the gate with its name.
	var changed []repositoryedit.ChangedFile
	if ch != nil {
		// The checked commit's own diff and ignore rules, never the folder's
		// HEAD now or its live .gitignore, from the change's base: after it
		// caught up with main, what it merged from main is not its scope.
		// A commit the hub does not hold yet is read from the baseline, and
		// an error leaves the baseline as it was (it can only be empty).
		scopeBase, baseErr := scm.ChangeBase(task.Context(), source.Repository(a.dataDir), source.DefaultBranch, commit, baseSHA)
		if baseErr != nil {
			scopeBase = baseSHA
		}
		if changed, err = repositoryedit.ChangedFilesBetween(task.Context(), checkedAt.Repo, scopeBase, commit, src); err != nil {
			finish(workflow.StatusError, map[string]any{"reason": "reading what the checked commit changed: " + err.Error()})
			return
		}
		target := workflow.AcceptanceScopeOf(ch)
		unmet := unmetAcceptance(results, target)
		// Scope (scope.go): a remediation that moved the dependencies has
		// widened beyond the one check it exists to close.
		if files := repositoryedit.DependencyChangesOutOfScope(changed, target); len(files) > 0 {
			unmet = append(unmet, "scope: "+strings.Join(files, ", ")+" changed — dependencies (beyond adding a package the fix needs) are not this change's to move; restore them exactly as they were on the base and commit only the fix")
		}
		if len(unmet) > 0 {
			evidence["summary"] = fmt.Sprintf("acceptance: %d check(s) this change exists to close did not pass", len(unmet))
			evidence["findings"] = unmet
			if onlyForgeFaults(results, unmet) {
				evidence["summary"] = "acceptance: AppTown could not judge the check this change exists to close"
				evidence["reason"] = "AppTown could not probe the change (" + strings.Join(unmet, "; ") + ") — not the change's fault; run this step again, or ask AppTown's operator"
				finish(workflow.StatusError, evidence)
				return
			}
			evidence["reason"] = "the change's own acceptance checks must pass outright: " + strings.Join(unmet, "; ")
			rework(evidence)
			return
		}
	}
	// Scope (scope.go), every change: what the repository's own ignore
	// rules cover is not source, whatever the language calls its build
	// folder — the project said so, git is the reference.
	if files := repositoryedit.IgnoredChanges(changed); len(files) > 0 {
		evidence["summary"] = fmt.Sprintf("scope: %d file(s) the repository ignores were committed", len(files))
		evidence["findings"] = []string{"ignored files committed: " + strings.Join(files, ", ")}
		evidence["reason"] = "the repository's own .gitignore says these are not source (build output, or AppTown runtime files under .apptown/): " + strings.Join(files, ", ") + " — remove them from git (git rm --cached <path>) and commit only the fix"
		rework(evidence)
		return
	}
	if failing > 0 {
		evidence["summary"] = fmt.Sprintf("guidance conformance: %d deterministic check(s) failed", failing)
		evidence["findings"] = failingTitles
		evidence["reason"] = fmt.Sprintf("%d guidance finding(s) this change introduced at/above %s — fix, or disposition each with a justification", failing, cfg.FailOn)
		if n := len(already) + len(undecided); n > 0 {
			evidence["reason"] = evidence["reason"].(string) + fmt.Sprintf("; %d other finding(s) already fail on main, or could not be compared with it, and are not this change's to fix", n)
		}
		rework(evidence)
		return
	}
	// A required runtime check AppTown probed and could not judge holds the
	// step for a person: counting findings alone would let a static page
	// served no values pass. An exception
	// accepts a known finding, never an unknown. A landing boots its
	// candidate's preview for these, and holds the same way when it cannot
	// judge them; a lane with no preview to probe records them unjudged.
	if len(unjudged) > 0 {
		evidence["summary"] = fmt.Sprintf("guidance conformance: AppTown could not judge %d required runtime check(s)", len(unjudged))
		evidence["findings"] = unjudged
		evidence["reason"] = "AppTown could not judge the running preview (" + strings.Join(unjudged, "; ") + ") — not the change's fault; run this step again, or ask AppTown's operator"
		finish(workflow.StatusError, evidence)
		return
	}
	switch {
	case len(undecided) > 0:
		evidence["summary"] = fmt.Sprintf("guidance conformance: nothing this change is known to have introduced; %d finding(s) could not be decided against main (each says why) — the reviewer judges the diff", len(undecided))
	case len(already) > 0:
		evidence["summary"] = fmt.Sprintf("guidance conformance: nothing this change introduced; %d finding(s) already on main, left for the fleet review", len(already))
	}
	finish(workflow.StatusPass, evidence)
}

// evaluatorsSaid is what the gate's evaluators said about the declared checks.
type evaluatorsSaid struct {
	results  Results
	findings []conformance.Finding
	errors   int      // sh and tool checks that errored
	unjudged []string // required runtime checks AppTown could not judge
	llm      map[string]any
}

// judgeDeclared runs every declared check through the runner for its
// evaluator kind. Each runner records one result for each check of its
// kind, so a declared check left without a result is one no runner here
// judges: the gate refuses it rather than let it pass unexamined.
func (a *Guidance) judgeDeclared(ctx context.Context, scope conformance.RunScope, run *workflow.Run, source workflow.Source, checks []conformance.DeclaredCheck, at checked, failOn, prefix string) (evaluatorsSaid, error) {
	var j evaluatorsSaid
	j.results, j.findings, j.errors = a.evaluator.runShChecks(ctx, checks, at.Src, prefix)

	// runtime checks at the gate are judged against the CHANGE'S OWN preview,
	// which the boot step leaves warm — never the standing deployment, which
	// still runs the code the change exists to replace (a readiness
	// remediation could not otherwise pass its own target until it landed).
	rtResults, rtFindings, unjudged := a.runRuntimeChecksForRun(ctx, run, checks, at.Src, at.Commit, failOn, at.Candidate)
	j.results = append(j.results, rtResults...)
	j.findings = append(j.findings, rtFindings...)
	j.unjudged = unjudged

	// tool checks (design/subsystems/conformance.md §13): source scanners run against the
	// staged tree; exec-image self-checks run inside the app image, shared
	// (content-addressed by commit) with apptown check so it is not built twice.
	// The gate runs the full set (AllowBuild).
	toolResults, toolFindings, toolErrors := a.scanners.RunToolChecks(ctx, checks,
		ToolContext{Context: conformance.WithRun(ctx, scope), ProjectID: run.ProjectID, SHA: at.Commit, BuildDir: at.Src, AllowBuild: true}, prefix)
	j.results = append(j.results, toolResults...)
	j.findings = append(j.findings, toolFindings...)
	j.errors += toolErrors

	llmResults, llmFindings, llmMeta := a.runLLMChecklist(ctx, source, checks, at)
	j.results = append(j.results, llmResults...)
	j.findings = append(j.findings, llmFindings...)
	j.llm = llmMeta

	var unexamined []string
	for _, c := range checks {
		if !slices.ContainsFunc(j.results, func(r workflow.CheckReceipt) bool {
			return r.Block == c.Block && r.Version == c.Version && r.Check == c.Check.Name
		}) {
			unexamined = append(unexamined, fmt.Sprintf("%s v%d · %s (evaluator %q)", c.Block, c.Version, c.Check.Name, check.Evaluator(c.Check)))
		}
	}
	if len(unexamined) > 0 {
		return j, fmt.Errorf("no evaluator at this gate judges %s; the check cannot be passed over", strings.Join(unexamined, ", "))
	}
	return j, nil
}
