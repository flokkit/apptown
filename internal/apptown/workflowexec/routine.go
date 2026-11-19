package workflowexec

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

type updateBounds interface {
	UpdateChoicesForRun(context.Context, ids.ID) (workflow.JanitorChoices, error)
}
type updateJudge interface {
	JudgeUpdate(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error)
}
type routineConfig struct {
	Rubric string `json:"rubric"`
}

func (a *commands) runUpdateRoutine(task *workflow.Execution) {
	if _, err := task.Claim(); err != nil {
		return
	}
	var classified []fix.File
	var receipt workflow.CheckReceipt
	var readings []imagelines.Reading
	finish := func(status workflow.StepState, why string) {
		verdict := workflow.VerdictError
		switch status {
		case workflow.StatusPass:
			verdict = workflow.VerdictPass
		case workflow.StatusFail:
			verdict = workflow.VerdictFail
		case workflow.StatusNotApplicable:
			verdict = workflow.VerdictNotApplicable
		}
		if status == workflow.StatusError && receipt.Verdict == workflow.VerdictCannotAssess {
			verdict = workflow.VerdictCannotAssess
		}
		receipt.Check, receipt.Evaluator, receipt.Verdict, receipt.Detail = task.Step().Name, "routine", verdict, why
		facts := workflow.CheckEvidence{Checks: []workflow.CheckReceipt{receipt}}
		extra := map[string]any{"reason": why, "files": classified, "imageReadings": readings}
		task.Settle(facts.Outcome(status, extra))
	}
	fail := func(err error) { receipt.AppTownFault = false; finish(workflow.StatusError, err.Error()) }
	if a.bounds == nil {
		fail(fmt.Errorf("update bounds are unavailable"))
		return
	}
	choices, err := a.bounds.UpdateChoicesForRun(task.Context(), task.Run().ID)
	if err != nil {
		fail(err)
		return
	}
	bound := choices.Dependencies
	if bound == "" || bound == "off" {
		finish(workflow.StatusNotApplicable, "no eligible update rule")
		return
	}
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: task.Run().ID, ProjectID: task.Run().ProjectID})
	if err != nil {
		fail(err)
		return
	}
	sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
	if err != nil {
		fail(err)
		return
	}
	base, err := scm.ForkBase(task.Context(), sel.Repo, sel.Repo, source.DefaultBranch, sel.Commit, source.ChangeBaseSHA)
	if err != nil {
		fail(err)
		return
	}
	changes, err := scm.ReadObjectChanges(task.Context(), sel.Repo, base, sel.Commit)
	if err != nil {
		fail(err)
		return
	}
	targets, err := runTargets(task.Run())
	if err != nil {
		fail(err)
		return
	}

	// The routine judges the change. An empty change has no diff to judge;
	// whether it may close is decided by AppTown's own steps (tests, start-up,
	// scans), never by the agent's account of why nothing moved.
	if len(changes) == 0 {
		finish(workflow.StatusNotApplicable, "nothing changed; there is no diff to judge")
		return
	}
	ds, err := a.declarations(task.Context(), task.Run())
	if err != nil {
		fail(err)
		return
	}
	var inputs [2]map[string]any
	for i, commit := range []string{base, sel.Commit} {
		tree, remove, err := verification.StageCommittedTree(task.Context(), a.cfg.DataDir, sel.Repo, commit)
		if err != nil {
			fail(err)
			return
		}
		inputs[i], err = verification.UpdateInputs(tree)
		remove()
		if err != nil {
			fail(err)
			return
		}
	}
	paths := make([]string, 0, len(changes))
	for _, f := range changes {
		paths = append(paths, f.Path)
	}
	classified = fix.Classify(paths, ds, inputs[:]...)
	queue, err := task.IsQueue()
	if err != nil {
		fail(err)
		return
	}
	if queue {
		main, err := gitexec.HubReadRef(task.Context(), sel.Repo, "refs/heads/"+source.DefaultBranch)
		if err != nil {
			fail(err)
			return
		}
		tree, err := gitexec.HubRun(task.Context(), sel.Repo, "rev-parse", sel.Commit+"^{tree}")
		if err != nil {
			fail(err)
			return
		}
		same, why, err := task.KeepProposal(main, strings.TrimSpace(tree), classified)
		if err != nil {
			fail(err)
			return
		}
		if same {
			task.Settle(workflow.EncodeOutcome(workflow.StatusSkipped, map[string]any{"reason": why, "sameProposal": true}, nil))
			if err := task.StopDuplicate(why); err != nil {
				return
			}
			return
		}
	}
	if err := verification.CompareUpdateInputs(task.Context(), inputs[0], inputs[1], choices.BaseImages, verification.ImageUpdates{Resolver: a.images, Scope: verification.UpdateScope(bound), Security: len(targets) > 0, ToolchainPatches: choices.ToolchainPatches, Readings: &readings}); err != nil {
		if fault.KindOf(err) == fault.Unavailable {
			receipt.Verdict = workflow.VerdictCannotAssess
			finish(workflow.StatusError, err.Error())
		} else {
			finish(workflow.StatusFail, err.Error())
		}
		return
	}
	var cfg routineConfig
	if err := workflow.DecodeStepConfig(task.Step().Config, &cfg); err != nil {
		fail(err)
		return
	}
	if strings.TrimSpace(cfg.Rubric) == "" {
		fail(fmt.Errorf("routine needs a dependency update rubric"))
		return
	}
	facts := "\n\nApproved level: " + bound
	if bound == "aggressive" {
		facts += " (including majors)"
	} else {
		facts += " (patch and minor)"
	}
	if choices.ToolchainPatches {
		facts += "\nToolchain patches: on"
	} else {
		facts += "\nToolchain patches: off"
	}
	if len(targets) > 0 {
		facts += "\nFindings to fix:"
		for _, t := range targets {
			if t.Package != nil {
				facts += fmt.Sprintf(" %s %s (%s)", t.Package.Name, t.Package.Version, strings.Join(t.Identifiers, ", "))
			}
		}
	}
	facts += "\nChanged files:"
	for _, class := range []string{"dependency", "toolchain", "other"} {
		var names []string
		for _, f := range classified {
			if f.Class == class {
				names = append(names, f.Path)
			}
		}
		if len(names) > 0 {
			facts += "\n  " + class + ": " + strings.Join(names, ", ")
		}
	}
	diff, err := gitexec.Run(task.Context(), sel.Repo, "diff", base+".."+sel.Commit)
	if err != nil {
		fail(err)
		return
	}
	declaration := check.Declared{Identity: check.Identity{Namespace: task.Run().WorkflowName, Version: task.Run().WorkflowVersion}, Declaration: check.Declaration{Name: task.Step().Name, Expects: "The dependency update stays within its approved level and scope.", Evaluator: "llm", Scope: check.ScopeDiff, Rubric: cfg.Rubric + facts}}
	item := check.DiffItems([]check.Declared{declaration}, diff)[0]
	item.Repository, item.Commit = sel.Repo, sel.Commit
	if a.judge == nil {
		fail(fmt.Errorf("dependency update judge is unavailable"))
		return
	}
	receipt, err = a.judge.JudgeUpdate(task.Context(), source, item, diff)
	if err != nil {
		fail(err)
		return
	}
	last, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
	if err != nil || !reflect.DeepEqual(sel, last) {
		fail(fmt.Errorf("the candidate moved while it was classified"))
		return
	}
	status := workflow.StatusError
	switch receipt.Verdict {
	case workflow.VerdictPass:
		status = workflow.StatusPass
	case workflow.VerdictFail:
		status = workflow.StatusFail
	}
	finish(status, receipt.Detail)
}
