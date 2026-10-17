package workflow

import (
	"context"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// StopWork is an explicit user request. Actor attribution and project scope are
// supplied by authority. Stopping a landing returns its change to work, the
// same whoever stops it: a person, a send-back, or taking it to chat.
type StopWork struct {
	RunID, ChangeID ids.ID
	Reason          string
}

func (s *Service) Abandon(ctx context.Context, in StopWork) (*Abandoned, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	command := Abandonment{RunID: in.RunID, ChangeID: in.ChangeID, Reason: in.Reason}
	if err := validateAbandonment(command); err != nil {
		return nil, err
	}
	var project ids.ID
	var actor commandActor
	var err error
	if !in.RunID.IsZero() {
		project, actor, err = s.runScope(ctx, in.RunID, actionStopRun)
	} else {
		project, actor, err = s.changeScope(ctx, in.ChangeID, actionCloseChange)
	}
	if err != nil {
		return nil, err
	}
	command.Who = actor.Receipt
	return s.abandonments.abandon(ctx, command, project)
}

// StopScope is a pre-admitted transaction participant. It holds repository
// serialization until Close; obtain it before opening the caller transaction.
// The caller owns authorization of the surrounding operation, commit and rollback.
type StopScope struct {
	owner   Abandonments
	project ids.ID
	who     string
	closure *scm.Closure
}

func (s *Service) PrepareStop(ctx context.Context, project ids.ID) (*StopScope, error) {
	actor, err := s.requireActor(ctx, operationPermission{Action: actionIdentify})
	if err != nil {
		return nil, err
	}
	closure, err := scm.PrepareClosure(ctx, s.abandonments.locks, project)
	if err != nil {
		return nil, err
	}
	return &StopScope{owner: s.abandonments, project: project, who: actor.Receipt, closure: closure}, nil
}

func (s *StopScope) Close() { s.closure.Close() }
func (s *StopScope) AbandonIn(ctx context.Context, tx *database.Tx, in StopWork) (*Abandoned, error) {
	return s.owner.abandonIn(ctx, tx, Abandonment{RunID: in.RunID, ChangeID: in.ChangeID, Who: s.who, Reason: in.Reason}, s.project, s.closure)
}

// Exactly one target: stop a run tree, or close an entire change.
type Abandonment struct {
	failedLandingOnly bool
	RunID             ids.ID
	ChangeID          ids.ID
	Who               string
	Reason            string
	// closedAs is set only by the engine's nothing-to-change close
	// (endNothingToChange); nothing a caller sends can set it.
	closedAs string
}

// ClosedNothingToChange is Change.ClosedAs for a change the engine closed
// because its run found nothing to change.
const ClosedNothingToChange = "nothing-to-change"

type Abandoned struct {
	RunIDs []ids.ID
}

type AbandonConflict struct{ message string }

func (e *AbandonConflict) Error() string         { return e.message }
func (e *AbandonConflict) FaultKind() fault.Kind { return fault.Conflict }

type Abandonments struct {
	db      *database.DB
	effects AbandonRecorder
	locks   *scm.RepositoryLocks
	clocked
}

// abandon owns the durable operation. It never executes Git, removes files,
// tears down containers, or calls back into App while the transaction is open.
func (s Abandonments) abandon(ctx context.Context, req Abandonment, projectID ids.ID) (*Abandoned, error) {
	if err := validateAbandonment(req); err != nil {
		return nil, err
	}
	if s.effects == nil {
		return nil, fmt.Errorf("abandonment effects are unavailable")
	}
	if projectID.IsZero() {
		q := workflowsql.New(s.db.SQL())
		var err error
		if !req.RunID.IsZero() {
			projectID, err = q.RunProjectMeta(ctx, req.RunID)
		} else {
			projectID, err = q.ChangeProjectMeta(ctx, req.ChangeID)
		}
		if err != nil {
			return nil, err
		}
	}
	closure, err := scm.PrepareClosure(ctx, s.locks, projectID)
	if err != nil {
		return nil, err
	}
	defer closure.Close()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Abandoned, error) { return s.abandonIn(ctx, tx, req, projectID, closure) })
}

// Join the caller transaction and acquire the shared project lock (normally
// already held by send-back/remediation). The caller owns commit/rollback.
func (s Abandonments) abandonIn(ctx context.Context, tx *database.Tx, req Abandonment, projectID ids.ID, closure *scm.Closure) (*Abandoned, error) {
	now := s.now()
	if projectID.IsZero() || s.effects == nil {
		return nil, fmt.Errorf("abandonment needs project scope and effects")
	}
	if err := validateAbandonment(req); err != nil {
		return nil, err
	}
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
		return nil, err
	}
	var err error
	var root *Run
	changeID := req.ChangeID
	if !req.RunID.IsZero() {
		if _, err := q.ScopedRunForUpdate(ctx, workflowsql.ScopedRunForUpdateParams{ID: req.RunID, ProjectID: projectID}); err != nil {
			return nil, err
		}
		root, err = readScopedRun(ctx, tx, req.RunID, projectID)
		if err != nil {
			return nil, err
		}
		if root.Status != RunRunning {
			return nil, &AbandonConflict{fmt.Sprintf("run %d is %s — only running runs can be abandoned", root.ID, root.Status)}
		}
		if req.failedLandingOnly {
			spec, err := root.Definition()
			if err != nil {
				return nil, err
			}
			var eligible bool
			if root.ParentRunID.IsZero() && spec.Role == "landing" {
				eligibleRow, err := q.AbandonFailedLandingEligible(ctx, root.ID)
				eligible = eligibleRow.Bool
				if err != nil {
					return nil, err
				}
			}
			if !eligible {
				return nil, &AbandonConflict{message: "landing is no longer blocked on failed gates; inspect before starting again"}
			}
		}
		changeID = root.ChangeID
		if !changeID.IsZero() {
			if _, err := readChangeInProject(ctx, tx, changeID, projectID); err != nil {
				return nil, err
			}
		}
	} else {
		change, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: changeID, ProjectID: projectID})
		if err != nil {
			return nil, err
		}
		state, err := ParseChangeState(change.State)
		if err != nil {
			return nil, err
		}
		if state.Closed() {
			return nil, &AbandonConflict{fmt.Sprintf("change #%d already %s", changeID, state)}
		}
	}
	if err := checkClosureIn(ctx, tx, closure, projectID, changeID, req.RunID); err != nil {
		return nil, err
	}
	// Traverse every descendant, even through a previously finished run.
	// Only running rows are changed; completed history remains untouched.
	result := &Abandoned{RunIDs: []ids.ID{}}
	var records []workflowsql.Run
	if !req.ChangeID.IsZero() {
		records, err = q.AbandonActiveChangeTrees(ctx, workflowsql.AbandonActiveChangeTreesParams{Status: RunRunning.String(), ChangeID: changeID, ProjectID: projectID})
	} else {
		records, err = q.AbandonActiveRunTree(ctx, workflowsql.AbandonActiveRunTreeParams{Status: RunRunning.String(), RootID: req.RunID, ProjectID: projectID})
	}
	if err != nil {
		return nil, err
	}
	active := make([]Run, 0, len(records))
	for _, row := range records {
		run, err := runRecord(row, nil)
		if err != nil {
			return nil, err
		}
		result.RunIDs = append(result.RunIDs, run.ID)
		active = append(active, *run)
	}
	for _, run := range active {
		id := run.ID
		if err := TransitionRunIn(ctx, tx, &run, RunAbandoned, run.CurrentStage, now); err != nil {
			return nil, err
		}
		payload := abandonedRunReceipt{By: req.Who, Reason: req.Reason}
		if !req.RunID.IsZero() {
			payload.Root = req.RunID
		} else {
			payload.Change = changeID
		}
		if err := recordReceiptIn(ctx, tx, id, "run.abandoned", payload, now); err != nil {
			return nil, err
		}
		// A call step has no runner to return: its child, in this same
		// tree, is abandoned here too, so the call ends now. Other running
		// steps end when their runners return (endAbandonedAttempt).
		steps, err := readSteps(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			if step.Type == "workflow/call" && step.Status == StatusRunning {
				if err := endAbandonedStepIn(ctx, tx, id, step, now); err != nil {
					return nil, err
				}
			}
		}
	}
	if !req.ChangeID.IsZero() {
		if _, err := TransitionChangeIn(ctx, tx, projectID, changeID, ChangeMove{Action: AbandonChange}, now); err != nil {
			return nil, err
		}
		if req.closedAs != "" {
			n, err := q.MarkChangeClosedAs(ctx, workflowsql.MarkChangeClosedAsParams{ClosedAs: req.closedAs, ID: changeID, ProjectID: projectID, State: ChangeAbandoned.String()})
			if err != nil {
				return nil, err
			}
			if n != 1 {
				return nil, fmt.Errorf("change %s: closed as %s marked %d rows", changeID, req.closedAs, n)
			}
		}
		if err := audit.At(now).RecordIn(ctx, tx, req.Who, projectID, "change.abandoned", fmt.Sprintf("change %d", changeID), abandonAudit{Reason: req.Reason, ClosedAs: req.closedAs}); err != nil {
			return nil, err
		}
	} else {
		if root.ParentRunID.IsZero() && !changeID.IsZero() {
			spec, err := root.Definition()
			if err != nil {
				return nil, err
			}
			if spec.Role == "landing" {
				if _, err := TransitionChangeIn(ctx, tx, projectID, changeID, ChangeMove{Action: CancelChangeLanding}, now); err != nil {
					return nil, err
				}
			}
		}
		if err := audit.At(now).RecordIn(ctx, tx, req.Who, projectID, "run.abandoned", fmt.Sprintf("run %d", root.ID), abandonAudit{Reason: req.Reason, Runs: result.RunIDs}); err != nil {
			return nil, err
		}
	}
	effects := AbandonEffects{ProjectID: projectID, RunID: req.RunID, ChangeID: changeID, RunIDs: append([]ids.ID{}, result.RunIDs...), CloseChange: !req.ChangeID.IsZero(), Root: root != nil && root.ParentRunID.IsZero()}
	if err := s.effects.RecordAbandonIn(ctx, tx, effects); err != nil {
		return nil, err
	}
	return result, nil
}

type AbandonEffects struct {
	ProjectID, RunID, ChangeID ids.ID
	RunIDs                     []ids.ID
	CloseChange, Root          bool
}

type AbandonRecorder interface {
	RecordAbandonIn(context.Context, *database.Tx, AbandonEffects) error
}

func NewAbandonments(db *database.DB, recorder AbandonRecorder, locks *scm.RepositoryLocks) Abandonments {
	return Abandonments{db: db, effects: recorder, locks: locks}
}

// Abandon is an explicitly pre-admitted execution path.
// Caller-facing operations enter Service, which supplies stored scope and authority.
func (s Abandonments) Abandon(ctx context.Context, in Abandonment) (*Abandoned, error) {
	return s.abandon(ctx, in, ids.Nil)
}

// Work owns run membership; SCM owns whether any of those runs has crossed
// its irreversible boundary. Include historical runs, not just active roots.
func checkClosureIn(ctx context.Context, tx *database.Tx, closure *scm.Closure, project, change, run ids.ID) error {
	idList := []ids.ID{}
	if !change.IsZero() {
		rows, err := workflowsql.New(tx.SQL()).ChangeRuns(ctx, workflowsql.ChangeRunsParams{ProjectID: project, ChangeID: change})
		if err != nil {
			return err
		}
		for _, row := range rows {
			idList = append(idList, row.ID)
		}
	} else if !run.IsZero() {
		idList = append(idList, run)
	}
	if err := closure.CheckIn(ctx, tx, project, idList); err != nil {
		if errors.Is(err, scm.ErrClosurePending) || errors.Is(err, scm.ErrClosureRecorded) {
			return &AbandonConflict{err.Error()}
		}
		return err
	}
	return nil
}

func validateAbandonment(in Abandonment) error {
	if (in.RunID.IsZero()) == (in.ChangeID.IsZero()) {
		return fmt.Errorf("abandonment needs exactly one run or change target")
	}
	return nil
}

type abandonedRunReceipt struct {
	By     string `json:"by"`
	Reason string `json:"reason"`
	Root   ids.ID `json:"root,omitzero"`
	Change ids.ID `json:"change,omitzero"`
}

type abandonAudit struct {
	Reason   string   `json:"reason"`
	ClosedAs string   `json:"closedAs,omitempty"`
	Runs     []ids.ID `json:"runs,omitzero"`
}

// SupersedeFailedLanding rechecks the preflight candidate while holding the
// project and Run locks. A resumed or completed landing cannot be stopped by a
// stale observation from definition resolution.
func (s Abandonments) SupersedeFailedLanding(ctx context.Context, ref RunRef, nextWorkflow string) (*Abandoned, error) {
	if ref.ProjectID.IsZero() || ref.ID.IsZero() {
		return nil, fmt.Errorf("supersession requires Run scope")
	}
	return s.abandon(ctx, Abandonment{RunID: ref.ID, Who: "forge-system", Reason: fmt.Sprintf("landing wedged on failed gates; superseded by a new %s run", nextWorkflow), failedLandingOnly: true}, ref.ProjectID)
}
