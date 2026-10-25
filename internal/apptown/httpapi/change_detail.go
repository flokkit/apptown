package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

type ChangeDetail struct {
	work       *workflow.Service
	adrs       *adrs.Service
	deployment *deployment.Service
	db         *database.DB
	usage      *usage.Ledger
	dataDir    string
}

func NewChangeDetail(work *workflow.Service, adrs *adrs.Service, deployment *deployment.Service, db *database.DB, usage *usage.Ledger, dataDir string) ChangeDetail {
	return ChangeDetail{work: work, adrs: adrs, deployment: deployment, db: db, usage: usage, dataDir: dataDir}
}
func (h ChangeDetail) GetChange(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	item, err := h.work.Change(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	slugMeta, slugErr := project.ByIDIn(r.Context(), h.db, item.ProjectID)
	slug := slugMeta.Slug
	if _, err := database.Lookup(slugErr); err != nil {
		WriteFault(w, err)
		return
	}
	var detail *workflow.RunSnapshot
	if !item.RunID.IsZero() {
		detail, err = h.work.Snapshot(r.Context(), item.RunID, workflow.DetailView)
		if err != nil {
			WriteFault(w, err)
			return
		}
	}
	adrRecords, err := h.adrs.ForChange(r.Context(), item.ProjectID, id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	state, stateDetail := workflow.ActivityState(""), ""
	if detail != nil {
		state, stateDetail = detail.State, detail.StateDetail
	}
	if item.State == workflow.ChangeAbandoned {
		state, stateDetail = workflow.ActivityAbandoned, item.AbandonReason
	} else if item.State == workflow.ChangeInProgress && detail == nil {
		stateDetail = "No workflow has started yet."
	}
	// Cockpit payload (design/subsystems/dev-loop.md): every root run, the branch's
	// commits, a diff stat, and the latest change report as the evidence-
	// derived "so far" until narrative distillation lands.
	runs := []ChangeRunSummary{}
	history, err := h.work.ChangeRuns(r.Context(), id, workflow.ChangeRuns{RootsOnly: true})
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, run := range history {
		rl := ChangeRunSummary{ID: run.ID, Number: run.Number, WorkflowName: run.WorkflowName, Status: run.Status, CurrentStage: run.CurrentStage, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
		var spec schema.Spec
		if json.Unmarshal([]byte(run.DefinitionJSON), &spec) == nil {
			rl.Role = spec.Role
		}
		runs = append(runs, rl)
	}
	// The change's spending is every run's, not the current run's: a
	// landed change is on its landing run, which spent next to nothing.
	roots := make([]ids.ID, 0, len(history)+1)
	for _, run := range history {
		roots = append(roots, run.ID)
	}
	if !item.RunID.IsZero() && !slices.Contains(roots, item.RunID) {
		roots = append(roots, item.RunID)
	}
	usageTotal, usageCost, err := h.usage.RunTotal(r.Context(), roots...)
	if err != nil {
		WriteFault(w, err)
		return
	}
	commits := []ChangeCommit{}
	head := ""
	diffstat := ""
	comparison := CommittedComparison{Status: "unavailable", Message: "No committed comparison is available yet."}
	var hub string
	if slug != "" {
		hub, err = slugMeta.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
	}
	if slug != "" && workflow.CarriesNoCode(item, history) && !branchPublished(r.Context(), hub, item.Branch) {
		var diff scm.CommittedDiff
		var ok bool
		comparison, diff, ok = shippedComparison(r.Context(), h.deployment.Releases, hub, item)
		if ok {
			diffstat, head = diff.Stat, diff.Head
			commits = commitsBetween(r.Context(), hub, diff.Base, diff.Head)
		}
	} else if item.Branch != "" && slug != "" {
		workspace := filepath.Join(h.dataDir, "workspaces", fmt.Sprintf("change-%d", id))
		var diff scm.CommittedDiff
		repo, readErr := scm.ComparisonRepository(r.Context(), hub, workspace)
		if readErr == nil {
			diff, readErr = scm.ReadChangeDiff(r.Context(), hub, repo, slugMeta.DefaultBranch, item.BaseSHA, item.Branch)
		}
		if readErr != nil {
			comparison.Message = "Committed comparison unavailable: " + readErr.Error()
		} else {
			comparison.Status, comparison.Message = "changed", ""
			if diff.Empty {
				comparison.Status, comparison.Message = "empty", workflow.EmptyCommittedChange
			}
			diffstat = diff.Stat
			head = diff.Head
			commits = commitsBetween(r.Context(), repo, diff.Base, diff.Head)
		}
	}

	// Main AppTown could not merge in before a step, while the branch
	// still does not contain it: the person catches up in the change's chat.
	// It is read across the change's runs: a landing refused since is the
	// change's current run and records none.
	var mainConflict *repositoryedit.MainConflict
	if item.State == workflow.ChangeInProgress && item.Branch != "" && slug != "" {
		latest, err := h.work.LatestChangeEvent(r.Context(), id, repositoryedit.MainConflictNotice)
		if err != nil {
			WriteFault(w, err)
			return
		}
		mainConflict = repositoryedit.MainConflictIn(r.Context(), hub, item.Branch, latest)
	}

	reports, err := h.work.ChangeArtifacts(r.Context(), id, workflow.ArtifactSelection{Kinds: []string{"change-report"}, Latest: true})
	if err != nil {
		WriteFault(w, err)
		return
	}
	report := ""
	if len(reports) > 0 {
		report = reports[0].Body
	}
	// Uncommitted worktree edits (chat sessions edit in place; commits come
	// at session end) — without this, a change being actively edited says
	// "no edits yet", which reads as broken.
	editing := 0
	if ws := filepath.Join(h.dataDir, "workspaces", fmt.Sprintf("change-%d", id)); hasGitDirectory(ws) {
		if out, err := gitexec.Run(r.Context(), ws, "status", "--porcelain"); err == nil {
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if l != "" && !strings.Contains(l, ".apptown/") {
					editing++
				}
			}
		}
	}
	evidence, err := h.evidenceForChange(r.Context(), item)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ChangeCockpit{Change: *item, ProjectSlug: slug, Detail: detail, ADRs: adrRecords, Evidence: evidence,
		Runs: runs, Commits: commits, Head: head, Diffstat: diffstat, CommittedDiff: comparison, Report: report, Editing: editing,
		State: state, StateDetail: stateDetail, Approval: h.approvalStanding(r, detail), MainConflict: mainConflict,
		Usage: ChangeUsage{Total: usageTotal, CostMicros: usageCost}, Next: workflow.ChangeActs(item, detail, evidence.Merged)})
}

// approvalStanding: whether the viewer could decide the approval the run
// waits on, so the page can say "waiting on you" only when it is true.
// Asking records nothing; a refusal is "no" with the rule's reason, and a
// failure to read the rules is "unknown", never yes.
func (h ChangeDetail) approvalStanding(r *http.Request, detail *workflow.RunSnapshot) *ApprovalStanding {
	if detail == nil {
		return nil
	}
	for _, act := range detail.Next {
		if act.Act != workflow.NextApprove {
			continue
		}
		standing := &ApprovalStanding{StepID: act.StepID, Viewer: "yes"}
		if err := h.work.MayApprove(r.Context(), act.StepID); err != nil {
			var denied *workflow.DecisionDenied
			if errors.As(err, &denied) {
				standing.Viewer, standing.Reason = "no", denied.Reason
			} else {
				standing.Viewer, standing.Reason = "unknown", err.Error()
			}
		}
		if question, err := h.work.ApprovalQuestion(r.Context(), act.StepID); err != nil {
			standing.AddsError = err.Error()
		} else {
			standing.Adds = question.Adds
		}
		return standing
	}
	return nil
}

// The Change page's cockpit (design/subsystems/dev-loop.md): every root run,
// the branch's commits, a diff stat, and the latest change report.
type (
	ChangeCockpit struct {
		Change      workflow.Change       `json:"change"`
		ProjectSlug string                `json:"projectSlug"`
		Detail      *workflow.RunSnapshot `json:"detail"`
		ADRs        []adrs.ChangeSummary  `json:"adrs"`
		Evidence    changeEvidence        `json:"evidence"`
		Runs        []ChangeRunSummary    `json:"runs"`
		Commits     []ChangeCommit        `json:"commits"`
		// Head is the change's current commit, the one its diff and
		// commits read (full); empty when it could not be read. A
		// preview's or a screenshot's commit is compared with it.
		Head          string                 `json:"head,omitempty"`
		Diffstat      string                 `json:"diffstat"`
		CommittedDiff CommittedComparison    `json:"committedDiff"`
		Report        string                 `json:"report"`
		Editing       int                    `json:"editing"`
		State         workflow.ActivityState `json:"state"`
		StateDetail   string                 `json:"stateDetail"`
		Usage         ChangeUsage            `json:"usage"`
		// Next is what a person can do on the change itself (land, send
		// back), by Work's rule; the run's own acts are on Detail.
		Next []workflow.NextAct `json:"next"`
		// Approval is the viewer's standing on the approval the run waits
		// on; absent when none waits.
		Approval *ApprovalStanding `json:"approval,omitempty"`
		// MainConflict is main AppTown could not merge into the change
		// before a step, while the branch does not contain it yet; absent
		// otherwise.
		MainConflict *repositoryedit.MainConflict `json:"mainConflict,omitempty"`
	}
	// ApprovalStanding: Viewer is "yes" when the viewer may decide StepID,
	// "no" when the rules refuse them (Reason says which rule), "unknown"
	// when the rules could not be read.
	ApprovalStanding struct {
		StepID ids.ID `json:"stepId"`
		Viewer string `json:"viewer"`
		Reason string `json:"reason,omitempty"`
		// Adds is what the approval's commit brings into the app, when it
		// assents to a commit; AddsError says why it could not be read.
		Adds      *workflow.ChangeAdds `json:"adds,omitempty"`
		AddsError string               `json:"addsError,omitempty"`
	}
	ChangeRunSummary struct {
		ID           ids.ID            `json:"id"`
		Number       int64             `json:"number"` // within the project
		WorkflowName string            `json:"workflowName"`
		Status       workflow.RunState `json:"status"`
		CurrentStage string            `json:"currentStage"`
		Role         string            `json:"role,omitempty"`
		CreatedAt    string            `json:"createdAt"`
		UpdatedAt    string            `json:"updatedAt"`
	}
	ChangeCommit struct {
		SHA     string `json:"sha"`
		Subject string `json:"subject"`
		When    string `json:"when"`
	}
	// CommittedComparison's Status is "changed", "empty", "unavailable", or
	// "no-code": a change with no code of its own, which Shipped describes.
	CommittedComparison struct {
		Status  string             `json:"status"`
		Message string             `json:"message,omitempty"`
		Shipped *ShippedComparison `json:"shipped,omitempty"`
	}
	ChangeUsage struct {
		Total      int64 `json:"total"`
		CostMicros int64 `json:"costMicros"`
	}
)

// commitsBetween is the commits after base up to head, newest first; none
// when the log cannot be read (the comparison already said what it could).
func commitsBetween(ctx context.Context, repo, base, head string) []ChangeCommit {
	commits := []ChangeCommit{}
	out, err := gitexec.Run(ctx, repo, "log", "--format=%h%x09%s%x09%cr", base+".."+head, "--", ".")
	if err != nil {
		return commits
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 {
			commits = append(commits, ChangeCommit{SHA: parts[0], Subject: parts[1], When: parts[2]})
		}
	}
	return commits
}
