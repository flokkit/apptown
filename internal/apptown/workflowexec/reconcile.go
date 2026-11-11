package workflowexec

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The land preflight (design/architecture/contracts.md dev-loop): before a
// change lands, does its branch contain where main is NOW? The landing's
// admission (and a recheck) has already merged main in when the branch was
// behind and the merge was clean (workcommands catchUp), so this gate only
// reads: a branch that contains main is pinned as the landing's candidate,
// so every gate below checks the tree that will be on main. A branch behind
// main here means main moved after the landing was asked for, or the chat
// published again; the landing ends, and asking again merges the newer
// main (stock/docs/guides/changes-and-main.md, "Landing: main merged in,
// then the gates").

// RunReconcile pins the change's branch as published as the landing's
// candidate when it already contains the hub's main.
func (a *repositories) RunReconcile(task *workflow.Execution) {
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
		// back in progress: asked again, the landing merges main in, or,
		// when main conflicts, the change's chat catches up.
		reason, evidence := behindMain(task.Context(), hub, candidate, mainBranch, branch)
		if task.Settle(workflow.EncodeOutcome(workflow.StatusFail, evidence, nil)) == nil {
			if err := task.EndLanding(reason); err != nil {
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

// behindMain is why a landing of a branch behind main ended, as the change
// page shows it once the landing has ended, and the step's evidence: the
// files that conflict, when main does not merge in cleanly, or else that
// main moved after the landing was asked for (its admission merges main in).
func behindMain(ctx context.Context, hub, candidate, mainBranch, branch string) (string, map[string]any) {
	catchUp := repositoryedit.CatchUpCommand(mainBranch, branch)
	reason := fmt.Sprintf("%[1]s moved after this landing was asked for, so the change no longer contains it. Check and land again: AppTown merges %[1]s in when it can.", mainBranch)
	evidence := map[string]any{"mainBranch": mainBranch}
	if merge, err := scm.MainConflicts(ctx, hub, candidate, mainBranch); err == nil && len(merge.Conflicts) > 0 {
		reason = fmt.Sprintf("%[1]s has moved and conflicts with this change in %[2]s, so it cannot land yet. Resolve it in the change's chat: catch up with %[1]s there, then land again.", mainBranch, strings.Join(merge.Conflicts, ", "))
		evidence["conflicts"] = merge.Conflicts
	}
	evidence["reason"] = reason + fmt.Sprintf(" The landing changed nothing. A chat can also catch up itself (%s).", catchUp)
	return reason, evidence
}
