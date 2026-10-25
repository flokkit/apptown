package workflowexec

import (
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The land preflight (design/architecture/contracts.md dev-loop): before a
// change lands, does its branch contain where main is NOW, or only the main
// it branched from? A change can sit for hours while main moves under it;
// discovering that at the final merge, after tests, scans and a human
// approval, is late. This gate runs FIRST and only reads: a branch that
// already contains main is pinned as the landing's candidate, so every gate
// below checks the tree that will be on main; a branch behind main fails
// here, and the change's worker (usually its chat) catches up and lands
// again. A landing never merges or publishes
// (stock/docs/guides/changes-and-main.md, "Landing: already caught up, then
// the gates").

// RunReconcile pins the change's branch as published as the landing's
// candidate when it already contains the hub's main.
func (a *Repositories) RunReconcile(task *workflow.Execution) {
	run := task.Run()
	finish := func(status workflow.StepState, evidence map[string]any) {
		task.Settle(workflow.EncodeOutcome(status, evidence, nil))
	}
	if _, err := task.Claim(); err != nil {
		return
	}
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if !source.RepoBacked() {
		finish(workflow.StatusPass, map[string]any{"reason": "not repo-backed — nothing to reconcile"})
		return
	}
	hub, err := source.Repository(a.cfg.DataDir)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	branch, mainBranch := source.Branch, source.DefaultBranch
	candidate, target := scm.Ref(task.Context(), hub, "refs/heads/"+branch), scm.Ref(task.Context(), hub, "refs/heads/"+mainBranch)
	if candidate == "" || target == "" {
		finish(workflow.StatusError, map[string]any{"reason": fmt.Sprintf("the project's repository has no %s or %s to check", branch, mainBranch)})
		return
	}
	if _, err := gitexec.Run(task.Context(), hub, "merge-base", "--is-ancestor", target, candidate); err != nil {
		// Nothing below can pass on a branch behind main, so the landing
		// ends here rather than running its other gates, and the change is
		// back in progress for its chat to catch up.
		if task.Settle(workflow.EncodeOutcome(workflow.StatusFail, behindMain(mainBranch, branch), nil)) == nil {
			if err := task.EndLanding(fmt.Sprintf("%s is behind %s", branch, mainBranch)); err != nil {
				log.Printf("run %d: ending the landing of a branch behind %s: %v", run.ID, mainBranch, err)
			}
		}
		return
	}
	reset, err := task.PinLandingCandidate(workflow.LandingCandidate{Candidate: candidate, Target: target})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "pinning the landing candidate: " + err.Error()})
		return
	}
	evidence := map[string]any{"reason": "contains " + mainBranch, "mainBranch": mainBranch,
		"candidate": candidate, "target": target, "head": strs.ShortSHA(candidate)}
	if len(reset) > 0 {
		evidence["recheckedGates"] = reset
	}
	finish(workflow.StatusPass, evidence)
}

func behindMain(mainBranch, branch string) map[string]any {
	return map[string]any{
		"mainBranch": mainBranch,
		"reason": fmt.Sprintf("behind %[1]s: %[1]s has moved ahead and %[2]s does not contain it. The landing ended here and changed nothing; "+
			"the change is back in progress. Catch up in the change's chat (%[3]s), then land again.", mainBranch, branch, repositoryedit.CatchUpCommand(mainBranch, branch)),
	}
}
