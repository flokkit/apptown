package workflowexec

import (
	"context"
	"fmt"
	"reflect"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

type updateBounds interface {
	UpdateBoundForRun(context.Context, ids.ID) (string, error)
}

func (a *commands) runUpdateRoutine(task *workflow.Execution) {
	if _, err := task.Claim(); err != nil {
		return
	}
	finish := func(status workflow.StepState, why string, moves []fix.Move) {
		verdict := workflow.VerdictPass
		switch status {
		case workflow.StatusFail:
			verdict = workflow.VerdictFail
		case workflow.StatusError:
			verdict = workflow.VerdictError
		case workflow.StatusNotApplicable:
			verdict = workflow.VerdictNotApplicable
		}
		facts := workflow.CheckEvidence{Checks: []workflow.CheckReceipt{{Check: task.Step().Name, Evaluator: "routine", Verdict: verdict, Detail: why}}}
		task.Settle(facts.Outcome(status, map[string]any{"reason": why, "packages": moves}))
	}
	if a.bounds == nil {
		finish(workflow.StatusError, "update bounds are unavailable", nil)
		return
	}
	bound, err := a.bounds.UpdateBoundForRun(task.Context(), task.Run().ID)
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	if bound == "" {
		finish(workflow.StatusNotApplicable, "no eligible update rule", nil)
		return
	}
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: task.Run().ID, ProjectID: task.Run().ProjectID})
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	base, err := scm.ForkBase(task.Context(), sel.Repo, sel.Repo, source.DefaultBranch, sel.Commit, source.ChangeBaseSHA)
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	files, err := scm.ReadObjectChanges(task.Context(), sel.Repo, base, sel.Commit)
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	if len(files) == 0 {
		finish(workflow.StatusNotApplicable, "nothing to change", nil)
		return
	}
	ds, err := a.declarations(task.Context(), task.Run())
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	executor := verification.Executor{Run: a.checks.Run, AgentImage: a.checks.Image}
	var listings [2][]verification.UpdateListing
	for i, commit := range []string{base, sel.Commit} {
		tree, remove, err := verification.StageCommittedTree(task.Context(), a.cfg.DataDir, sel.Repo, commit)
		if err != nil {
			finish(workflow.StatusError, err.Error(), nil)
			return
		}
		listings[i], err = verification.ListUpdates(task.Context(), a.db, executor, tree, ds)
		remove()
		if err != nil {
			finish(workflow.StatusError, err.Error(), nil)
			return
		}
	}
	if len(listings[0]) != len(listings[1]) {
		finish(workflow.StatusFail, "the parts or fix declarations changed", nil)
		return
	}
	targets, err := runTargets(task.Run())
	if err != nil {
		finish(workflow.StatusError, err.Error(), nil)
		return
	}
	var moved []fix.Move
	var beforePackages, afterPackages []fix.Listing
	for i, before := range listings[0] {
		after := listings[1][i]
		if before.Part != after.Part || before.Declaration != after.Declaration {
			finish(workflow.StatusFail, "the parts changed", nil)
			return
		}
		changes, err := fix.CompareUpdate(before.Packages, after.Packages, bound, nil)
		if err != nil {
			finish(workflow.StatusFail, fmt.Sprintf("part %s: %v", before.Part, err), nil)
			return
		}
		moved = append(moved, changes...)
		beforePackages = append(beforePackages, before.Packages...)
		afterPackages = append(afterPackages, after.Packages...)
	}
	if _, err := fix.CompareUpdate(beforePackages, afterPackages, bound, targets); err != nil {
		finish(workflow.StatusFail, err.Error(), nil)
		return
	}
	// Commit selection is repeated after external execution, fencing a moved candidate.
	last, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
	if err != nil || !reflect.DeepEqual(sel, last) {
		finish(workflow.StatusError, "the candidate moved while it was classified", nil)
		return
	}
	finish(workflow.StatusPass, "the update is within the selected bound", moved)
}
