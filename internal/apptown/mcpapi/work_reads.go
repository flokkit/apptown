package mcpapi

import (
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type WorkReadsDependencies struct {
	Scope         Scope
	Work          *workflow.Service
	DB            *database.DB
	Contributions *repositoryedit.Contributions
}
type workReads struct {
	scope         Scope
	work          *workflow.Service
	db            *database.DB
	contributions *repositoryedit.Contributions
}

func NewWorkReads(deps WorkReadsDependencies) []Tool {
	h := workReads{scope: deps.Scope, work: deps.Work, db: deps.DB, contributions: deps.Contributions}
	return []Tool{{Name: "list_changes",
		order: 4, Description: "Returns the project's Changes, newest first, optionally only those in one state (in_progress, landing, shipped, abandoned).", InputSchema: schemaObject(map[string]any{"slug": slugArg, "state": schemaString("Only Changes in this state: in_progress, landing, shipped or abandoned. Omit for all.")}, "slug"), command: ownerCommand("work_reads.listChanges", h.listChanges)}, {Name: "get_change",
		order: 5, Description: "Returns one Change with its current run (steps and their status), every run it has had, and what it is waiting on and on whom. When a person's approval of a commit is waiting, waiting.adds is what that commit brings into the app (secrets, services, app parts, and ways of working for an uplift), read from its declarations; addsError says why it could not be read.", InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"), command: ownerCommand("work_reads.getChange", h.getChange)},
		{
			Name:        "get_change_diff",
			order:       6,
			Description: "Returns the Change's whole diff against the commit it forked from, as unified diff text (cut at 200 KB with truncated: true).",
			InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"),
			command:     ownerCommand("work_reads.getChangeDiff", h.getChangeDiff),
		},
		{
			Name:        "get_change_evidence",
			order:       7,
			Description: "Returns what the Change's current run recorded: each step with a bounded summary of its evidence, and the run's artifacts (kind, name, reference) without their bodies. reportedByAgent is the agent's own report from request_change_landing, not checked by AppTown. summaryReadsRequestAs is the summary's reading of whether the request is met, with what falls short and what would satisfy it.",
			InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"),
			command:     ownerCommand("work_reads.getChangeEvidence", h.getChangeEvidence),
		},
		{
			Name:        "get_run",
			order:       8,
			Description: "Returns one workflow run: its status, its honest state (working, waiting on you, blocked), and every step with its status.",
			InputSchema: schemaObject(map[string]any{"id": schemaID("The run's id.")}, "id"),
			command:     ownerCommand("work_reads.getRun", h.getRun),
		},
		{
			Name:        "get_waiting_on_you",
			order:       9,
			Description: "Returns the approvals waiting on a person right now — each with its Change, the step, and what saying OK would mean — across every project you can see, or one project.",
			InputSchema: schemaObject(map[string]any{"slug": schemaString("Only this project's approvals. Omit for every project you can see.")}),
			command:     filteredCommand("work_reads.getWaitingOnYou", h.getWaitingOnYou),
		},
	}
}
func (h workReads) listChanges(c *Call, args map[string]any) (any, error) {
	p, err := h.scope.projectForRead(c, argString(args, "slug"))
	if err != nil {
		return nil, err
	}
	var state workflow.ChangeState
	if text := argString(args, "state"); text != "" {
		state, err = workflow.ParseChangeState(text)
		if err != nil {
			return nil, refuse("%v", err)
		}
	}
	items, err := h.work.Changes(c.Context, p.ID, workflow.ChangeList{State: state, Limit: 200, Summary: true})
	if err != nil {
		return nil, err
	}

	changes := []map[string]any{}
	for _, it := range items {
		changes = append(changes, map[string]any{
			"id": it.ID, "type": it.Type, "title": it.Title, "state": it.State, "branch": it.Branch, "baseSha": it.BaseSHA,
			"runId": it.RunID, "runStatus": it.RunStatus, "stage": it.Stage, "workflow": it.WorkflowName,
			"createdAt": it.CreatedAt, "updatedAt": it.UpdatedAt, "webUrl": c.changeURL(it.ID),
		})
	}

	return map[string]any{"project": p.Slug, "changes": changes}, nil
}
func (h workReads) getChange(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id": ch.ID, "project": p.Slug, "type": ch.Type, "title": ch.Title, "body": ch.Body,
		"state": ch.State, "branch": ch.Branch, "baseSha": ch.BaseSHA, "externalRef": ch.ExternalRef,
		"summary": ch.Summary, "provenance": ch.Provenance, "createdAt": ch.CreatedAt, "closedAt": ch.ClosedAt,
		"webUrl": c.changeURL(ch.ID),
	}
	if !ch.RunID.IsZero() {
		d, err := h.work.Snapshot(c.Context, ch.RunID, workflow.DetailView)
		if err != nil {
			return nil, err
		}
		out["run"] = c.mcpRunSummary(d, p.Slug)
		waiting := mcpWaiting(d)
		h.addsInto(c, waiting)
		out["waiting"] = waiting
	}
	if _, ok := out["waiting"]; !ok {
		out["waiting"] = map[string]any{"on": "nothing", "why": "no run is open on this Change"}
	}
	runs := []map[string]any{}
	history, err := h.work.ChangeRuns(c.Context, ch.ID, workflow.ChangeRuns{RootsOnly: true})
	if err != nil {
		return nil, err
	}
	for _, run := range history {
		runs = append(runs, map[string]any{"id": run.ID, "workflow": run.WorkflowName, "status": run.Status, "currentStage": run.CurrentStage, "createdAt": run.CreatedAt, "updatedAt": run.UpdatedAt})
	}
	out["runs"] = runs
	return out, nil
}
func (h workReads) getChangeDiff(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	diff, err := h.contributions.Diff(c.Context, ch.ID)
	if err != nil {
		return nil, contributionError(err)
	}
	const capBytes = 200 * 1024
	truncated := false
	if len(diff) > capBytes {
		diff, truncated = diff[:capBytes], true
	}
	return map[string]any{"id": ch.ID, "project": p.Slug, "branch": ch.Branch, "baseSha": ch.BaseSHA,
		"diff": diff, "bytes": len(diff), "truncated": truncated, "webUrl": c.changeURL(ch.ID) + "/diff"}, nil
}
func (h workReads) getChangeEvidence(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	runID := ch.RunID
	if runID.IsZero() {
		// A closed Change keeps no run_id; its latest root run is
		// the evidence people look at.
		history, err := h.work.ChangeRuns(c.Context, ch.ID, workflow.ChangeRuns{RootsOnly: true, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(history) > 0 {
			runID = history[0].ID
		}
	}
	if runID.IsZero() {
		return nil, refuse("Change %d has no run yet, so there is no evidence to show; start one with start_change_lane or request_change_landing", ch.ID)
	}
	d, err := h.work.Snapshot(c.Context, runID, workflow.DetailView)
	if err != nil {
		return nil, err
	}
	steps := make([]map[string]any, 0, len(d.Steps))
	for _, s := range d.Steps {
		st := map[string]any{"id": s.ID, "stage": s.Stage, "name": s.Name, "type": s.Type, "status": s.Status,
			"startedAt": s.StartedAt, "finishedAt": s.FinishedAt}
		if s.OverrideBy != "" {
			st["overriddenBy"], st["overrideReason"] = s.OverrideBy, s.OverrideReason
		}
		if ev := mcpEvidenceSummary(string(s.Evidence)); ev != nil {
			st["evidence"] = ev
		}
		steps = append(steps, st)
	}
	artifacts := make([]map[string]any, 0, len(d.Artifacts))
	var reported map[string]any
	for _, ar := range d.Artifacts {
		if ar.ReportedByAgent() {
			// Last one wins; shown apart from what AppTown checked.
			reported = map[string]any{"text": ar.Body, "createdAt": ar.CreatedAt,
				"note": "reported by the agent; not checked by AppTown and never counted by a gate"}
			if int64(len(ar.Body)) < ar.BodyBytes {
				reported["note"] = fmt.Sprintf("%v; shown here are the first %d of %d bytes", reported["note"], len(ar.Body), ar.BodyBytes)
			}
			continue
		}
		artifacts = append(artifacts, map[string]any{"id": ar.ID, "kind": ar.Kind, "name": ar.Name, "ref": ar.Ref,
			"bytes": ar.BodyBytes, "createdAt": ar.CreatedAt,
			"apiUrl": fmt.Sprintf("%s/api/v1/artifacts/%d", c.BaseURL, ar.ID)})
	}
	out := map[string]any{"id": ch.ID, "project": p.Slug, "runId": d.Run.ID, "workflow": d.Run.WorkflowName,
		"status": d.Run.Status, "currentStage": d.Run.CurrentStage, "steps": steps, "artifacts": artifacts,
		"webUrl": c.changeURL(ch.ID)}
	if reported != nil {
		out["reportedByAgent"] = reported
	}
	if ch.RequestMet != "" {
		// The summary's reading of the request; the gap says what fell short
		// and what would satisfy it, so an agent can act on it.
		out["summaryReadsRequestAs"] = map[string]any{"met": ch.RequestMet, "gap": ch.RequestGap}
	}
	return out, nil
}
func (h workReads) getRun(c *Call, args map[string]any) (any, error) {
	run, p, err := h.scope.runForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	d, err := h.work.Snapshot(c.Context, run.ID, workflow.DetailView)
	if err != nil {
		return nil, err
	}
	out := c.mcpRunSummary(d, p.Slug)
	out["project"] = p.Slug
	if !run.ChangeID.IsZero() {
		if ch, err := workflow.ReadChangeIn(c.Context, h.db, run.ChangeID); err == nil {
			out["change"] = map[string]any{"id": ch.ID, "title": ch.Title, "state": ch.State, "webUrl": c.changeURL(ch.ID)}
		}
	}
	return out, nil
}
func (h workReads) getWaitingOnYou(c *Call, args map[string]any) (any, error) {
	var selectedProject ids.ID
	if slug := argString(args, "slug"); slug != "" {
		p, err := h.scope.projectForRead(c, slug)
		if err != nil {
			return nil, err
		}
		selectedProject = p.ID
	}
	inbox, err := h.work.ApprovalInbox(c.Context, selectedProject)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, pending := range inbox {
		stepID, runID, changeID := pending.Step.ID, pending.Run.ID, pending.Run.ChangeID
		stepName, stage, workflowName := pending.Step.Name, pending.Step.Stage, pending.Run.WorkflowName
		title, slug, projectName := pending.ChangeTitle, pending.ProjectSlug, pending.ProjectName
		// Already voted: it is not waiting on this person any more.
		ev, err := workflow.ReadApprovalEvidence(pending.Step.Evidence)
		if err != nil {
			return nil, err
		}
		if ev.HasVoted(c.principal().ID) {
			continue
		}
		cfg, err := workflow.ReadApprovalConfig(pending.Step.Config)
		if err != nil {
			return nil, err
		}
		item := map[string]any{
			"stepId": stepID, "step": stepName, "stage": stage, "means": cfg.Means, "runId": runID, "workflow": workflowName,
			"project": slug, "projectName": projectName, "changeId": changeID, "changeTitle": title,
		}
		// Who the workflow names as approver, when it names anyone —
		// the reader decides whether that is them; the
		// authorization kernel will filter here later.
		if len(cfg.Approvers) > 0 {
			item["approvers"] = cfg.Approvers
		}
		if !changeID.IsZero() {
			item["webUrl"] = c.changeURL(changeID)
		} else {
			item["webUrl"] = c.projectURL(slug)
		}
		items = append(items, item)
	}
	return map[string]any{"user": c.principal().Name, "waiting": items}, nil
}

// addsInto puts what a waiting approval's commit adds beside it, so an
// agent can say what a person will be asked to accept. A list that could
// not be read says so; it is never left out as if nothing were added.
func (h workReads) addsInto(c *Call, waiting map[string]any) {
	id, ok := waiting["stepId"].(ids.ID)
	if !ok || waiting["on"] != "a person" {
		return
	}
	q, err := h.work.ApprovalQuestion(c.Context, id)
	switch {
	case err != nil:
		waiting["addsError"] = err.Error()
	case q.Adds != nil:
		waiting["adds"] = q.Adds
	}
}
