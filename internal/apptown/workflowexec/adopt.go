package workflowexec

import (
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
)

// The adoption operation has recorded the frozen target at Change admission.
// The declared fix rechecks it and fingerprints it before tests start.
func (a *commands) runAdopt(task *workflow.Execution, classify bool) {
	if _, err := task.Claim(); err != nil {
		return
	}
	finish := func(status workflow.StepState, why string) {
		verdict := workflow.VerdictPass
		if status == workflow.StatusError {
			verdict = workflow.VerdictError
		}
		if status == workflow.StatusFail {
			verdict = workflow.VerdictFail
		}
		ev := workflow.CheckEvidence{Checks: []workflow.CheckReceipt{{Check: task.Step().Name, Evaluator: "routine", Verdict: verdict, Detail: why}}}
		task.Settle(ev.Outcome(status, map[string]any{"reason": why}))
	}
	target, err := task.CheckAdoptionTarget()
	if err != nil {
		finish(workflow.StatusFail, err.Error())
		return
	}
	queue, err := task.IsQueue()
	if err != nil {
		finish(workflow.StatusError, err.Error())
		return
	}
	if !classify && queue {
		if err := task.StartFix(nil); err != nil {
			finish(workflow.StatusError, err.Error())
			return
		}
		if err := repositoryedit.CommitStep(task.Context(), a.db, a.cfg.DataDir, a.branches, a.publisher, task.Run(), task.Step(), a.now()); err != nil {
			finish(workflow.StatusError, err.Error())
			return
		}
		source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: task.Run().ID, ProjectID: task.Run().ProjectID})
		if err != nil {
			finish(workflow.StatusError, err.Error())
			return
		}
		sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
		if err != nil {
			finish(workflow.StatusError, err.Error())
			return
		}
		main, err := gitexec.HubReadRef(task.Context(), sel.Repo, "refs/heads/"+source.DefaultBranch)
		if err != nil {
			finish(workflow.StatusError, err.Error())
			return
		}
		same, why, err := task.KeepPackProposal(main, target.CandidateDigest)
		if err != nil {
			finish(workflow.StatusError, err.Error())
			return
		}
		if same {
			if err := task.StopDuplicate(why); err != nil {
				finish(workflow.StatusError, err.Error())
			}
			return
		}
	}
	finish(workflow.StatusPass, "the adoption target is still the assessed candidate")
}
