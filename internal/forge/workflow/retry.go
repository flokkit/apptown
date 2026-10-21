package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type RetryMode uint8

const (
	RetryDefault RetryMode = iota
	RetryContinue
	RetryRestart
)

func (m RetryMode) valid() bool { return m <= RetryRestart }

func (m RetryMode) String() string {
	switch m {
	case RetryDefault:
		return ""
	case RetryContinue:
		return "continue"
	case RetryRestart:
		return "restart"
	default:
		return "invalid-retry-mode"
	}
}

func ParseRetryMode(s string) (RetryMode, error) {
	switch s {
	case "":
		return RetryDefault, nil
	case "continue":
		return RetryContinue, nil
	case "restart":
		return RetryRestart, nil
	default:
		return 0, &RetryError{RetryInvalid, "retry mode must be continue, restart, or empty"}
	}
}

type RetryFailure uint8

const (
	RetryInvalid RetryFailure = iota + 1
	RetryConflict
	RetryUnavailable
)

type RetryError struct {
	Kind   RetryFailure
	Reason string
}

func (e *RetryError) Error() string { return e.Reason }

// FaultKind: a step that cannot be retried now is unprocessable, a raced retry
// is a conflict, and anything else is the request's fault.
func (e *RetryError) FaultKind() fault.Kind {
	switch e.Kind {
	case RetryUnavailable:
		return fault.Unprocessable
	case RetryConflict:
		return fault.Conflict
	}
	return fault.Invalid
}

type RetryCommand struct {
	StepID   ids.ID
	Mode     RetryMode
	Feedback string
	// AcceptUncopiedData is a person's choice, on retrying a deploy step
	// refused because the copy taken before its release was incomplete,
	// to release anyway. It is bound to this attempt: a later retry
	// without it accepts nothing.
	AcceptUncopiedData bool
}

// UncopiedDataAcceptance is what a deploy step's configuration carries for
// an attempt retried accepting uncopied data: who, and when.
type UncopiedDataAcceptance struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
	// Attempt is the attempt it was given for: the step's next.
	Attempt int64 `json:"attempt"`
}

const uncopiedDataKey = "acceptUncopiedData"

type Retried struct{ RunID, StepID ids.ID }

// DispatchCommittedRetry is the transaction owner's post-commit handoff for
// RecheckLandingIn. Interactive callers use complete commands instead.
func (s *Service) DispatchCommittedRetry(runID ids.ID) {
	if s.dispatcher.isStopped() {
		return
	}
	plan, err := s.dispatchPlans.Plan(s.life, runID)
	if err != nil {
		s.dispatcher.report(fmt.Errorf("run %d: retry committed; dispatch needs recovery: %w", runID, err))
		return
	}
	s.dispatcher.DispatchAgain(plan)
}

// RetryStep authorizes the observed step, commits its guarded reset and receipts,
// then schedules dispatch. No runner starts inside the transaction.
func (s *Service) RetryStep(ctx context.Context, in RetryCommand) (Retried, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return Retried{}, err
	}
	p, err := (stepDecisionStore{db: s.db, clocked: s.clocked}).prepare(ctx, in.StepID)
	if err != nil {
		return Retried{}, err
	}
	actor, err := s.requireActor(ctx, operationPermission{Action: actionRetryAttempt, ProjectID: p.scope.ProjectID, ChangeID: p.scope.ChangeID, RunID: p.scope.RunID})
	if err != nil {
		return Retried{}, err
	}
	// Releasing over data AppTown could not copy is a person's own
	// decision, as an approval is: never through a chat, an agent's
	// connection or a run step acting for them.
	if in.AcceptUncopiedData {
		c, _ := access.CallerFrom(ctx)
		if who := c.Principal.Actor(); !c.Verified || !who.Assents() {
			return Retried{}, &RetryError{RetryInvalid, fmt.Sprintf("releasing over data AppTown could not copy is a person's own decision, given in their browser or with their command-line token, not %s", wayWords(who))}
		}
	}
	result, err := s.stepRetries.retryExpected(ctx, retryIntent{RetryCommand: in, RunID: p.scope.RunID, Actor: actor}, &p)
	if err == nil {
		s.DispatchCommittedRetry(result.RunID)
	}
	return result, err
}

// retryIntent is already admitted work. User commands go through Service;
// attribution alone is never permission. RunID optionally binds a transaction participant.
type retryIntent struct {
	RetryCommand
	RunID ids.ID
	Actor commandActor
}

type stepRetries struct {
	db        *database.DB
	automatic []string
	clocked
}

func newStepRetries(db *database.DB, automatic []string) stepRetries {
	return stepRetries{db: db, automatic: slices.Clone(automatic)}
}

func (s stepRetries) retryExpected(ctx context.Context, req retryIntent, expected *stepDecisionSnapshot) (Retried, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Retried, error) { return s.retryLocked(ctx, tx, req, expected, now) })
}

// RetryIn composes an already-admitted send-back with the same checked
// writer, at the send-back's time now: the retry's writes and receipts are
// the send-back's.
func (s stepRetries) RetryIn(ctx context.Context, tx *database.Tx, req retryIntent, now time.Time) (Retried, error) {
	return s.retryLocked(ctx, tx, req, nil, now)
}

func (s stepRetries) retryLocked(ctx context.Context, tx *database.Tx, req retryIntent, expected *stepDecisionSnapshot, now time.Time) (Retried, error) {
	run, step, err := lockStep(ctx, tx, req.StepID)
	if err != nil {
		return Retried{}, err
	}
	if err := refuseUnlessActive(ctx, tx, run.ProjectID); err != nil {
		return Retried{}, err
	}
	if expected != nil && !expected.matches(run, step) {
		return Retried{}, &RetryError{RetryConflict, "step changed after retry authorization"}
	}
	if (!req.RunID.IsZero() && req.RunID != run.ID) || run.Status != RunRunning || run.CurrentStage != step.Stage {
		return Retried{}, &RetryError{RetryConflict, "this step is no longer in an active stage; use the change's send-back action to request more work"}
	}
	if err := s.retryIn(ctx, tx, run, step, req, now); err != nil {
		return Retried{}, err
	}
	return Retried{run.ID, step.ID}, nil
}

// retryIn writes the retry at the operation's time now.
func (s stepRetries) retryIn(ctx context.Context, tx *database.Tx, run *Run, step *Step, req retryIntent, now time.Time) error {
	if !slices.Contains(s.automatic, step.Type) {
		return &RetryError{RetryInvalid, "only automatic steps can be retried"}
	}
	if !req.Mode.valid() {
		return &RetryError{RetryInvalid, "retry mode must be continue, restart, or empty"}
	}
	if step.Type != "agent/pi" && (req.Mode != RetryDefault || strings.TrimSpace(req.Feedback) != "") {
		return &RetryError{RetryInvalid, "retry mode and feedback apply only to agent steps"}
	}
	// "deploy" is the landing's deploy step (workflowexec.DeployKind).
	if req.AcceptUncopiedData && step.Type != "deploy" {
		return &RetryError{RetryInvalid, "accepting uncopied data applies only to a deploy step"}
	}
	if step.Status == StatusPending || step.Status == StatusRunning {
		return &RetryError{RetryConflict, "this step is already queued or running"}
	}
	// Avoid changing the workspace under another runner, and keep immediate
	// dispatch and restart recovery subject to the same entry prerequisites.
	steps, err := stepRecords(workflowsql.New(tx.SQL()).StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: run.ID, Stage: step.Stage}))
	if err != nil {
		return err
	}
	for _, other := range steps {
		if other.Status == StatusRunning {
			return &RetryError{RetryConflict, "wait for the running steps to finish before retrying"}
		}
	}
	spec, err := run.Definition()
	if err != nil {
		return err
	}
	stage, _ := spec.Stage(step.Stage)
	if stage == nil {
		return fmt.Errorf("stage %q is missing from the pinned definition", step.Stage)
	}
	if entryStep(stage, step) {
		for _, prior := range steps {
			if prior.ID.Compare(step.ID) < 0 && entryStep(stage, prior) && prior.Status != StatusPass {
				return &RetryError{RetryConflict, fmt.Sprintf("retry %q first; this step depends on it", prior.Name)}
			}
		}
	}
	cfg, err := retryConfiguration(step.Config)
	if err != nil {
		return err
	}
	if step.Type == "agent/pi" && StartedUnderOldDockerRule(step.Config) {
		return &RetryError{RetryInvalid, OldDockerRuleRefusal}
	}
	// An acceptance is this attempt's alone: every retry takes the earlier
	// one away and records only its own.
	delete(cfg, uncopiedDataKey)
	if req.AcceptUncopiedData {
		body, err := json.Marshal(UncopiedDataAcceptance{By: req.Actor.Receipt, At: now.UTC(), Attempt: int64(step.Attempt) + 1})
		if err != nil {
			return err
		}
		cfg[uncopiedDataKey] = body
	}
	if step.Type == "agent/pi" {
		var previousFeedback string
		_ = json.Unmarshal(cfg["sendback"], &previousFeedback)
		delete(cfg, "retryMode")
		delete(cfg, "sendback")
		delete(cfg, "continueSession")
		if req.Mode != RetryDefault {
			setRetryField(cfg, "retryMode", req.Mode.String())
		}
		if req.Mode == RetryContinue {
			session, err := previousAgentSessionIn(ctx, tx, step)
			if err != nil {
				return err
			}
			if session != "" {
				setRetryField(cfg, "continueSession", session)
			}
		}
		if strings.TrimSpace(req.Feedback) != "" {
			setRetryField(cfg, "sendback", req.Feedback)
		} else if req.Mode == RetryContinue && previousFeedback != "" {
			setRetryField(cfg, "sendback", previousFeedback)
		} else if req.Mode == RetryContinue {
			// Freeze the feedback before invalidating the checks that supplied
			// it. Recovery must see the same message as immediate dispatch.
			feedback, err := workflowsql.New(tx.SQL()).LatestRetryFeedback(ctx, workflowsql.LatestRetryFeedbackParams{RunID: run.ID, Stage: step.Stage})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if feedback != "" {
				setRetryField(cfg, "sendback", "The previous check reported:\n"+feedback)
			}
		}
	}
	if step.Type == "agent/pi" || entryStep(stage, step) {
		if err := invalidateRetryDependentsIn(ctx, tx, stage, step, steps, now); err != nil {
			return err
		}
	}
	if _, err := step.Status.Transition(MoveStepReset, StatusPending); err != nil {
		return err
	}
	config, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := requireAttemptWrite(workflowsql.New(tx.SQL()).RetryStep(ctx, workflowsql.RetryStepParams{ConfigJson: config, ID: step.ID, Attempt: step.Attempt})); err != nil {
		return err
	}
	if err := invalidateRunDecisionIn(ctx, tx, run, now); err != nil {
		return err
	}
	if err := recordReceiptIn(ctx, tx, run.ID, "step.retried", retryReceipt{step.Name, step.ID, step.Stage, req.Actor.Receipt, req.Mode.String(), step.Status, step.Evidence}, now); err != nil {
		return err
	}
	if strings.TrimSpace(req.Feedback) != "" {
		return recordReceiptIn(ctx, tx, run.ID, "step.sendback", retryFeedback{step.Name, step.ID, req.Actor.Receipt, req.Feedback}, now)
	}
	return nil
}

// A changed entry result invalidates later entry work and all gate verdicts.
// Reset them before dispatch, so neither advancement nor assertion refresh
// mistakes old passes/skips for results of the new attempt.
func invalidateRetryDependentsIn(ctx context.Context, tx *database.Tx, stage *schema.Stage, source *Step, steps []*Step, at time.Time) error {
	for _, step := range steps {
		if step.ID == source.ID || (entryStep(stage, step) && step.ID.Compare(source.ID) < 0) {
			continue
		}
		if _, err := step.Status.Transition(MoveStepReset, StatusPending); err != nil {
			return err
		}
		if err := recordReceiptIn(ctx, tx, source.RunID, "step.invalidated", retryInvalidation{step.Name, step.ID, source.Name, step.Status, step.Evidence, step.OverrideBy}, at); err != nil {
			return err
		}
		// An acceptance was for the attempt it was given with: a step reset
		// through an earlier one is a new attempt, accepting nothing.
		cfg, err := retryConfiguration(step.Config)
		if err != nil {
			return err
		}
		if _, ok := cfg[uncopiedDataKey]; ok {
			delete(cfg, uncopiedDataKey)
			config, err := json.Marshal(cfg)
			if err != nil {
				return err
			}
			if err := requireAttemptWrite(workflowsql.New(tx.SQL()).RetryStep(ctx, workflowsql.RetryStepParams{ConfigJson: config, ID: step.ID, Attempt: step.Attempt})); err != nil {
				return err
			}
			continue
		}
		if err := requireAttemptWrite(workflowsql.New(tx.SQL()).InvalidateStep(ctx, workflowsql.InvalidateStepParams{ID: step.ID, Attempt: step.Attempt})); err != nil {
			return err
		}
	}
	return nil
}

func retryConfiguration(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = map[string]json.RawMessage{}
	}
	return cfg, nil
}

func setRetryField(cfg map[string]json.RawMessage, key, value string) {
	body, _ := json.Marshal(value)
	cfg[key] = body
}

type retryReceipt struct {
	Step             string          `json:"step"`
	StepID           ids.ID          `json:"stepId"`
	Stage            string          `json:"stage"`
	By               string          `json:"by"`
	Mode             string          `json:"mode"`
	PreviousStatus   StepState       `json:"previousStatus"`
	PreviousEvidence json.RawMessage `json:"previousEvidence"`
}

type retryFeedback struct {
	Step   string `json:"step"`
	StepID ids.ID `json:"stepId"`
	By     string `json:"by"`
	Note   string `json:"note"`
}

type retryInvalidation struct {
	Step               string          `json:"step"`
	StepID             ids.ID          `json:"stepId"`
	After              string          `json:"after"`
	PreviousStatus     StepState       `json:"previousStatus"`
	PreviousEvidence   json.RawMessage `json:"previousEvidence"`
	PreviousOverrideBy string          `json:"previousOverrideBy"`
}

// invalidateRunDecisionIn fences every previously observed advancement decision.
// The caller holds the Run lock and commits its reset/receipts in this transaction.
func invalidateRunDecisionIn(ctx context.Context, tx *database.Tx, run *Run, at time.Time) error {
	n, err := workflowsql.New(tx.SQL()).InvalidateRunDecision(ctx, workflowsql.InvalidateRunDecisionParams{At: at, ID: run.ID, CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRunMoved
	}
	return nil
}

// OldDockerRuleRefusal is said of an agent step admitted while private Docker
// was given to every skill/implementation step, before steps declared it.
const OldDockerRuleRefusal = "this run was started under the old private-Docker rule; start a new run"

// StartedUnderOldDockerRule: the step's frozen configuration is one the old
// rule gave private Docker (schema.UsesOldDockerRule). Its run is not
// rewritten, and it is not run under a rule it was not admitted with.
func StartedUnderOldDockerRule(config json.RawMessage) bool {
	var with map[string]any
	return json.Unmarshal(config, &with) == nil && schema.UsesOldDockerRule(with)
}
