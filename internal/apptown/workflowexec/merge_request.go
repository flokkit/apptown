package workflowexec

import (
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/scm"

	"github.com/flokkit/apptown/internal/apptown/changeactions"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// RunSCMMR executes an scm/mr step: push the run branch, open an MR, record
// it as evidence + artifact (the gate's materialization).
func (a *Repositories) RunSCMMR(task *workflow.Execution) {
	run := task.Run()
	if _, err := task.Claim(); err != nil {
		return
	}
	fail := func(reason string) {
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": reason}, nil))
	}
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		fail(err.Error())
		return
	}
	if !source.RepoBacked() {
		fail("project is not repo-backed")
		return
	}
	repo, branch := source.Repository(a.cfg.DataDir), source.Branch
	_, pathNS, hasRemote := scm.Origin(task.Context(), repo)
	if !hasRemote {
		fail("project has no remote; POST /projects/{slug}/repo/remote first")
		return
	}
	env, err := a.provider.AskpassEnv(task.Context())
	if err != nil {
		fail(err.Error())
		return
	}
	// The request offers the branch as published in the project's
	// repository, which holds the linked remote; a run's workspace is a
	// self-contained clone with no remote (workflow.GitWorkspace), and its
	// unpublished commits are not part of the change. Never forced: a
	// GitLab branch with commits AppTown did not publish refuses the push.
	ref := "refs/heads/" + branch
	if out, err := gitexec.RunEnv(task.Context(), repo, env, "push", "origin", ref+":"+ref); err != nil {
		fail("publishing the branch to the project's remote: " + strs.Truncate(out, 300))
		return
	}
	gl, err := a.provider.Client(task.Context())
	if err != nil {
		fail(err.Error())
		return
	}
	p, _ := project.ByIDIn(task.Context(), a.db, run.ProjectID)
	db := p.DefaultBranch
	// The description is AppTown's (scm.MergeRequestDescription): who
	// opened the change and what it is for, from the change's own record. A
	// change that cannot be read fails the step; an MR is never opened
	// without its opener.
	opening := scm.Attribution{Workflow: fmt.Sprintf("%s@v%d", run.WorkflowName, run.WorkflowVersion), RunID: run.ID, RunNumber: run.Number}
	title := fmt.Sprintf("%s run %d", run.WorkflowName, run.Number)
	if !run.ChangeID.IsZero() {
		item, err := workflow.ReadChangeIn(task.Context(), a.db, run.ChangeID)
		if err != nil {
			fail("reading the change this merge request is for: " + err.Error())
			return
		}
		title = item.Title
		opening.ChangeID, opening.ChangeNumber, opening.Title, opening.Body, opening.OpenedBy = item.ID, item.Number, item.Title, item.Body, item.OpenedBy
	}
	// The link is the instance's real front door, never a dev-box literal
	// (every MR used to point at localhost:7777).
	page := ""
	if !run.ChangeID.IsZero() {
		page = changeactions.URL(a.cfg.PublicHost, run.ChangeID)
	}
	desc := scm.MergeRequestDescription(opening, page)
	mr, err := gl.CreateMR(task.Context(), pathNS, branch, db, title, desc)
	if err != nil {
		fail(err.Error())
		return
	}
	// The merge request exists: its record is the step's evidence, written
	// before the step passes.
	if err := recordArtifact(task.Context(), a.db, run.ID, "mr", fmt.Sprintf("!%d", mr.IID), mr.WebURL, pathNS, a.now()); err != nil {
		fail(fmt.Sprintf("merge request %s was opened, but its record could not be written: %v", mr.WebURL, err))
		return
	}
	if err := task.Settle(workflow.EncodeOutcome(workflow.StatusPass, map[string]any{"url": mr.WebURL, "iid": mr.IID, "project": pathNS}, nil)); err != nil {
		return
	}
	recordEvent(task.Context(), a.db, run.ID, "mr.opened", map[string]any{"iid": mr.IID, "url": mr.WebURL}, a.now())
}
