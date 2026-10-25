package workflowexec

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/database"
	"strconv"

	"github.com/flokkit/apptown/internal/apptown/project"

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
	branch := source.Branch
	verified, err := a.attachments.Push(task.Context(), run.ProjectID, branch)
	if err != nil {
		fail(err.Error())
		return
	}
	gl := verified.Provider.Client
	providerID := strconv.FormatInt(verified.Identity.ProviderProjectID, 10)
	pathNS := verified.Observed.PathNS
	p, err := project.ByIDIn(task.Context(), a.db, run.ProjectID)
	if err != nil {
		fail(err.Error())
		return
	}
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
	mr, err := gl.CreateMR(task.Context(), providerID, branch, db, title, desc)
	if err != nil {
		fail(err.Error())
		return
	}
	// The merge request exists: its record is the step's evidence, written
	// before the step passes.
	settle := context.WithoutCancel(task.Context())
	if err := a.db.InTx(settle, func(tx *database.Tx) error {
		artifact, err := workflow.AddArtifactIn(settle, tx, run.ID, "mr", fmt.Sprintf("!%d", mr.IID), mr.WebURL, pathNS, a.now())
		if err != nil {
			return err
		}
		return scm.RecordMergeRequestIdentityIn(settle, tx, artifact, verified.Identity)
	}); err != nil {
		fail(fmt.Sprintf("merge request %s was opened, but its record could not be written: %v", mr.WebURL, err))
		return
	}
	if err := task.Settle(workflow.EncodeOutcome(workflow.StatusPass, map[string]any{"url": mr.WebURL, "iid": mr.IID, "project": pathNS}, nil)); err != nil {
		return
	}
	recordEvent(task.Context(), a.db, run.ID, "mr.opened", map[string]any{"iid": mr.IID, "url": mr.WebURL}, a.now())
}
