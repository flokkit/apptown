package workflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// PersonalOpenChange is the requester's current work, including changes with no run.
// State is one of running, waiting on you, waiting on someone else, or idle.
// Reason comes from Work; clients need not interpret run or approval records.
type PersonalOpenChange struct {
	ID          ids.ID            `json:"id"`
	Number      int64             `json:"number"`
	Title       string            `json:"title"`
	ProjectSlug string            `json:"projectSlug"`
	UpdatedAt   string            `json:"updatedAt"`
	State       personalOpenState `json:"state"`
	Reason      string            `json:"reason"`
	IdleSince   string            `json:"idleSince,omitempty"`
}

// OpenChanges returns at most 20 of the reader's unclosed changes, newest
// activity first, and the total in currently readable projects. Attribution is
// by principal, never by a display name, approval, or viewing history. Selection
// and activity share a read snapshot; current authority still judges approvals.
func (s *Service) OpenChanges(ctx context.Context) ([]PersonalOpenChange, int64, error) {
	actor, err := s.requireActor(ctx, operationPermission{Action: actionIdentify})
	if err != nil {
		return nil, 0, err
	}
	projects, err := s.visibleProjects(ctx, actionReadChange, ids.Nil)
	if err != nil {
		return nil, 0, err
	}
	out := []PersonalOpenChange{}
	if actor.Owner.Principal.IsZero() || len(projects) == 0 {
		return out, 0, ctx.Err()
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := workflowsql.New(tx.SQL()).PersonalOpenChanges(ctx, workflowsql.PersonalOpenChangesParams{
		Principal: actor.Owner.Principal, ProjectIds: projects, Running: RunRunning.String(), InProgress: ChangeInProgress.String(), Landing: ChangeLanding.String(),
	})
	if err != nil {
		return nil, 0, err
	}
	runs := make([]ids.ID, 0, len(rows))
	for _, row := range rows {
		if !row.RunID.IsZero() {
			runs = append(runs, row.RunID)
		}
	}
	graph, err := readWorkflowActivity(ctx, tx, runs)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	for _, row := range rows {
		total = row.Total
		at := row.UpdatedAt.Format(time.RFC3339Nano)
		item := PersonalOpenChange{ID: row.ID, Number: row.Number, Title: row.Title, ProjectSlug: row.ProjectSlug, UpdatedAt: at, State: openIdle, Reason: "No workflow is running", IdleSince: at}
		if row.RunID.IsZero() {
			ch, err := ReadChangeIn(ctx, tx, row.ID)
			if err != nil {
				return nil, 0, err
			}
			var current *RunSnapshot
			if !ch.RunID.IsZero() {
				current, err = readWorkflowSnapshot(ctx, tx, ch.RunID, DetailView, s.wait, ch.ProjectID, s.stepAttempts.automatic)
				if err != nil {
					return nil, 0, err
				}
			}
			switch {
			case ch.State == ChangeLanding:
				item.State, item.Reason = openRunning, "landing"
			case s.editingFiles(ctx, ch.ID) > 0:
				item.State, item.Reason = openRunning, "being edited in a chat"
			default:
				for _, act := range ChangeActs(ch, current, false, false) {
					if act.Act == NextLand {
						item.State, item.Reason = openWaitingReader, "ready to land"
					}
				}
			}
			if item.State != openIdle {
				item.IdleSince = ""
			}
		} else {
			activity, err := graph.classify(row.RunID, s.wait, make(map[ids.ID]bool))
			if err != nil {
				return nil, 0, err
			}
			item.IdleSince = ""
			item.State, item.Reason = openRunning, activity.Detail
			switch activity.State {
			case ActivityBlocked:
				item.State = openWaitingReader
			case ActivityReady:
				item.State, item.Reason = openWaitingReader, "Ready to advance"
			case ActivityWaiting:
				item.State, item.Reason, err = s.openApprovalState(ctx, tx, activity.Approval, actor.Owner.Principal)
				if err != nil {
					return nil, 0, err
				}
			}
		}
		out = append(out, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *Service) openApprovalState(ctx context.Context, tx *database.Tx, id, reader ids.ID) (personalOpenState, string, error) {
	q := workflowsql.New(tx.SQL())
	stored, err := q.StepByID(ctx, id)
	if err != nil {
		return 0, "", err
	}
	step, err := stepRecord(stored)
	if err != nil {
		return 0, "", err
	}
	cfg, err := ReadApprovalConfig(step.Config)
	if err != nil {
		return 0, "", err
	}
	run, err := q.RunByID(ctx, workflowsql.RunByIDParams{ID: step.RunID})
	if err != nil {
		return 0, "", err
	}
	eligibility := s.MayApprove(ctx, id)
	var denied *DecisionDenied
	var accessDenied *access.Denied
	if eligibility != nil && !errors.As(eligibility, &denied) && !errors.As(eligibility, &accessDenied) {
		return 0, "", eligibility
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil {
		return 0, "", err
	}
	if eligibility == nil && !evidence.HasVoted(reader) {
		return openWaitingReader, "Approve " + step.Name, nil
	}
	policy, err := q.ApproverEligibility(ctx, workflowsql.ApproverEligibilityParams{ProjectID: run.ProjectID, ChangeID: run.ChangeID})
	if err != nil {
		return 0, "", err
	}
	asked := approvalExpressions(cfg.Approvers, policy.DefaultApprovers)
	reason := "Approval needed for " + step.Name
	if len(asked) > 0 {
		reason += " from " + strings.Join(asked, ", ")
	} else {
		reason += "; no approvers are named"
	}
	if evidence.HasVoted(reader) {
		reason += "; you have already voted"
	}
	return openWaitingElsewhere, reason, nil
}
