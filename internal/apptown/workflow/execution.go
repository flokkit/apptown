package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// Runner performs external work. Execution owns its admitted input, delayed
// attempt claim and outcome. Returning never advances a stage: the dispatcher
// preserves entry ordering, scanner groups and child-call settlement.
type Runner func(*Execution)

// Execution is issued only by Work. A runner may wait for a resource before
// Claim; every claim rechecks the original step and active run under lock.
// The private claim prevents a tool from changing the identity it finishes.
type Execution struct {
	service  *Service
	ctx      context.Context
	step     *Step
	run      *Run
	claim    *Step
	finished bool
}

// fenceContext is the context the attempt's fenced transactions (claim,
// outcome, InAttempt) run under. Once Work has cancelled the step's context
// for a reason that is not AppTown stopping, they run detached, so the
// locked fence still gives its answer:
//   - the attempt was lost (lost_attempt.go): the fence answers
//     ErrStepAttemptLost, and a durable collector settles and releases what
//     it holds, rather than reading a cancellation it treats as a failed
//     settlement;
//   - the runner returned (errRunnerReturned): whoever kept the claimed
//     execution may still record its outcome, as before Work released the
//     context on return.
//
// Any other cancellation (AppTown stopping) is kept: it is not proof the
// attempt was lost.
func (e *Execution) fenceContext() context.Context {
	if e.detached() {
		return context.WithoutCancel(e.ctx)
	}
	return e.ctx
}

// detached reports whether Work itself ended the step's context.
func (e *Execution) detached() bool {
	cause := context.Cause(e.ctx)
	return errors.Is(cause, ErrStepAttemptLost) || errors.Is(cause, errRunnerReturned)
}

// fenced runs one of the attempt's fenced transactions and returns the
// fence's answer. If Work ends the step's context while the transaction
// runs (its cancellation interrupts it), the fence is asked once more,
// detached: an interrupted transaction committed nothing, and after a loss
// the answer is then ErrStepAttemptLost, which collectors settle on. A
// failure while Work has not ended the context is returned as it is.
func (e *Execution) fenced(op func(context.Context) error) error {
	ctx := e.fenceContext()
	if seam := e.service.beforeFence; seam != nil {
		seam(e)
	}
	err := op(ctx)
	if err != nil && !errors.Is(err, ErrStepAttemptLost) && ctx == e.ctx && e.detached() {
		err = op(context.WithoutCancel(e.ctx))
	}
	return err
}

// now is AppTown's clock, Work's.
func (e *Execution) now() time.Time { return e.service.now() }

// Context is the context the step runs under: work the step does on the
// AppTown's behalf (its model calls) ends with it.
func (e *Execution) Context() context.Context { return e.ctx }

func (e *Execution) Step() *Step {
	value := *e.step
	value.Config = append(json.RawMessage(nil), value.Config...)
	value.Evidence = append(json.RawMessage(nil), value.Evidence...)
	return &value
}

func (e *Execution) Run() *Run { value := *e.run; return &value }

// RunScope identifies this execution's run through a fresh read for cooperating domains.
func (e *Execution) RunScope() runScope { return e.service.RunScope(e.run.ID) }

func (e *Execution) Claim() (*Step, error) {
	if e.claim != nil {
		return nil, ErrStepAttemptLost
	}
	var claim *Step
	err := e.fenced(func(ctx context.Context) (err error) {
		claim, err = e.service.stepAttempts.start(ctx, e.step, e.step.Type, e.run.ProjectID)
		return err
	})
	if err != nil {
		e.service.dispatcher.report(fmt.Errorf("step %d: cannot start: %w", e.step.ID, err))
		return nil, err
	}
	e.claim = claim
	e.step = claim
	// A step of an eval's copy starts only while its eval may proceed: a
	// spent budget or a deleted eval stops the run here, waiting, and the
	// eval settles it.
	if gate := e.service.evalGate; gate != nil {
		why, err := gate.RefusalIn(e.ctx, e.service.db, e.run.ProjectID)
		if err != nil {
			return nil, err
		}
		if why != "" {
			outcome, encodeErr := EncodeOutcome(StatusNeedsHuman, map[string]string{"reason": why}, nil)
			if finishErr := e.Finish(outcome, encodeErr); finishErr != nil {
				return nil, finishErr
			}
			return nil, errors.New(why)
		}
	}
	// A deploy is an effect a person answers for (assent.go): without a
	// person's approval or a start by someone who may land, the step stops
	// here, waiting on a person, and nothing is deployed.
	if claim.Type == "deploy" {
		if err := assentEvidenceIn(e.ctx, e.service.db, e.run.ID, e.run.ProjectID); err != nil {
			if errors.Is(err, errNoAssent) {
				outcome, encodeErr := EncodeOutcome(StatusNeedsHuman, map[string]string{"reason": err.Error()}, nil)
				if finishErr := e.Finish(outcome, encodeErr); finishErr != nil {
					return nil, finishErr
				}
			}
			return nil, err
		}
	}
	return e.Step(), nil
}

// Current reports whether the claimed attempt is still the step's current
// one: the run is running in the step's stage, and the step is running
// under this attempt. A runner that waited after its claim (a recovered
// task waiting for its folder) asks before it acts.
func (e *Execution) Current() error {
	if e.claim == nil {
		return ErrStepAttemptLost
	}
	// Read past a cancelled context: Work cancels it once the attempt is
	// lost (lost_attempt.go), and the answer must still be "lost".
	ctx := context.WithoutCancel(e.ctx)
	step, err := ReadStepIn(ctx, e.service.db, e.claim.ID)
	if err != nil {
		return err
	}
	run, err := ReadRunIn(ctx, e.service.db, e.run.ID)
	if err != nil {
		return err
	}
	if run.Status != RunRunning || run.CurrentStage != step.Stage || step.Status != StatusRunning || step.Attempt != e.claim.Attempt {
		return ErrStepAttemptLost
	}
	return nil
}

// Finish accepts the encoder's result/error pair so malformed tool evidence
// cannot write an outcome. Failure and its requested rework commit together,
// as do an outcome and its artifacts; scheduling follows commit. Evidence refresh waits until the runner returns,
// after its artifacts and external observations have been published.
func (e *Execution) Finish(outcome StepOutcome, encodingError error) error {
	err := encodingError
	if err == nil {
		err = e.fenced(func(ctx context.Context) error { return e.service.finishAttempt(ctx, e.claim, outcome) })
	}
	if err != nil {
		e.service.dispatcher.report(fmt.Errorf("step %d: cannot record %s: %w", e.step.ID, outcome.Status, err))
		return err
	}
	e.finished = true
	if outcome.Status == StatusError {
		var evidence struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(outcome.Evidence, &evidence); err != nil {
			evidence.Reason = "step errored; its evidence could not be read: " + err.Error()
		} else if evidence.Reason == "" {
			evidence.Reason = "step errored"
		}
		e.service.dispatcher.report(&StepError{RunID: e.run.ID, StepID: e.step.ID, Kind: e.step.Type, Name: e.step.Name, Reason: evidence.Reason})
	}
	return nil
}

// endAbandonedAttempt settles an attempt whose runner has returned without
// recording an outcome because its run was abandoned meanwhile: the outcome
// fence refuses it, and nothing else would ever end the step, which would
// then show "running" in an abandoned run (a step claimed a moment before its
// run was abandoned). Only now, with the runner returned, does
// the step stop counting as work still running in the change's folder.
func (s *Service) endAbandonedAttempt(e *Execution) {
	if e.finished || e.claim == nil || e.claim.OwnedAttempt() == 0 {
		return
	}
	if err := s.stepAttempts.endAbandoned(context.WithoutCancel(e.ctx), e.claim); err != nil {
		s.dispatcher.report(fmt.Errorf("step %d: ending its attempt in an abandoned run: %w", e.claim.ID, err))
	}
}

// StepError is a committed error verdict, distinct from a failed database write.
type StepError struct {
	RunID, StepID      ids.ID
	Kind, Name, Reason string
}

func (e *StepError) Error() string { return fmt.Sprintf("step %d: %s", e.StepID, e.Reason) }

// ExecuteStep invokes one registered runner under explicit caller authority.
// It does not advance the stage; normal scheduling owns that ordering boundary.
func (s *Service) ExecuteStep(ctx context.Context, id ids.ID) error {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return err
	}
	step, err := readStep(ctx, s.db, id)
	if err != nil {
		return err
	}
	project, _, err := s.runScope(ctx, step.RunID, actionRunStep)
	if err != nil {
		return err
	}
	return s.runExecution(ctx, step, RunRef{ID: step.RunID, ProjectID: project})
}

func (s *Service) executeRunner(ctx context.Context, id ids.ID, kind string) error {
	step, err := readStep(ctx, s.db, id)
	if err != nil {
		return err
	}
	if step.Type != kind {
		return ErrStepAttemptLost
	}
	return s.runExecution(ctx, step, RunRef{})
}

func (s *Service) runExecution(ctx context.Context, step *Step, scope RunRef) error {
	run, err := readRun(ctx, s.db, step.RunID)
	if err != nil {
		return err
	}
	if !scope.ID.IsZero() && (run.ID != scope.ID || run.ProjectID != scope.ProjectID) {
		return ErrRunMoved
	}
	if step.Status != StatusPending || step.OverrideBy != "" || run.Status != RunRunning || run.CurrentStage != step.Stage {
		return ErrStepAttemptLost
	}
	if err := requireRunReady(ctx, s.db, run.ID); err != nil {
		return err
	}
	runner := s.runners[step.Type]
	if runner == nil {
		return fmt.Errorf("no runner for %s", step.Type)
	}
	stepCtx, unwatch := s.watchAttempt(s.stepContext(ctx, run.ProjectID), run.ID, step.ID)
	execution := &Execution{service: s, ctx: stepCtx, step: step, run: run}
	defer func() { unwatch(execution.claim != nil) }()
	runner(execution)
	s.endAbandonedAttempt(execution)
	// A finished step's evidence is refreshed though the engine is stopping.
	if execution.finished {
		if err := s.refreshAssertions(context.WithoutCancel(ctx), RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
			s.dispatcher.report(fmt.Errorf("run %d: refreshing runner evidence: %w", run.ID, err))
		}
	}
	return nil
}

// ClaimedStep supplies a bounded participant with the identity already admitted
// by Work. It cannot claim, finish, or retarget an execution. Completion still
// rechecks the original attempt through Finish.
func (e *Execution) ClaimedStep(kind string) (projectID, runID, stepID ids.ID, err error) {
	if e == nil || e.claim == nil || e.finished || e.claim.Type != kind {
		return ids.Nil, ids.Nil, ids.Nil, ErrStepAttemptLost
	}
	return e.run.ProjectID, e.run.ID, e.claim.ID, nil
}

// InAttempt runs write in one transaction that first confirms, under the
// step's lock, that this claimed attempt of kind still owns its step: the
// run is running in the step's stage, and the step is running at this
// attempt with no override. A participant writes only its own tables
// through tx, and commits with Work's check or not at all. A lost attempt
// is ErrStepAttemptLost, and write is not called.
func (e *Execution) InAttempt(kind string, write func(tx *database.Tx, attempt int64) error) error {
	if e == nil || e.claim == nil || e.finished || e.claim.Type != kind {
		return ErrStepAttemptLost
	}
	return e.fenced(func(ctx context.Context) error {
		return e.service.db.InTx(ctx, func(tx *database.Tx) error {
			run, current, err := lockStep(ctx, tx, e.claim.ID)
			if err != nil {
				return err
			}
			if run.Status != RunRunning || run.CurrentStage != current.Stage || current.Status != StatusRunning ||
				current.OverrideBy != "" || current.Attempt != e.claim.OwnedAttempt() {
				return ErrStepAttemptLost
			}
			return write(tx, current.Attempt)
		})
	})
}

func (s *Service) finishAttempt(ctx context.Context, claim *Step, outcome StepOutcome) error {
	evidence, err := s.placement.placed(claim.Type, outcome.Evidence)
	if err != nil {
		return fmt.Errorf("recording where the step ran: %w", err)
	}
	outcome.Evidence = s.masked(ctx, evidence)
	if !outcome.Rework {
		return s.stepAttempts.finish(ctx, claim, outcome)
	}
	if outcome.Status != StatusFail {
		return fmt.Errorf("rework requires a failed verdict")
	}
	result, err := (reworkStore{s.db, s.clocked}).fail(ctx, claim, outcome)
	if err == nil {
		s.dispatchRework(result)
	}
	return err
}

// RecoverExecution returns the execution of a step attempt a previous AppTown
// process admitted, so the task it started can be collected and the step
// settled (the first-slice plan, step 2). It grants no new attempt: the step
// must still be running at exactly that attempt, and the claim carries the
// branch authority recorded when the attempt was admitted.
func (s *Service) RecoverExecution(ctx context.Context, stepID ids.ID, attempt int64) (*Execution, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	return s.recoverExecution(ctx, stepID, attempt)
}

// recoverRunner hands a running step's recorded work to its kind's
// recoverer, from the dispatcher, as runExecution hands a pending one to its
// runner.
func (s *Service) recoverRunner(ctx context.Context, stepID ids.ID, kind string) error {
	step, err := readStep(ctx, s.db, stepID)
	if err != nil {
		return err
	}
	if step.Type != kind {
		return ErrStepAttemptLost
	}
	execution, err := s.recoverExecution(ctx, stepID, step.Attempt)
	if err != nil {
		return err
	}
	var unwatch func(bool)
	execution.ctx, unwatch = s.watchAttempt(execution.ctx, execution.run.ID, stepID)
	defer unwatch(true) // a recovered execution is claimed
	s.recoverers[kind](execution)
	s.endAbandonedAttempt(execution)
	if execution.finished {
		if err := s.refreshAssertions(context.WithoutCancel(ctx), RunRef{ID: execution.run.ID, ProjectID: execution.run.ProjectID}); err != nil {
			s.dispatcher.report(fmt.Errorf("run %d: refreshing runner evidence: %w", execution.run.ID, err))
		}
	}
	return nil
}

func (s *Service) recoverExecution(ctx context.Context, stepID ids.ID, attempt int64) (*Execution, error) {
	got, err := database.Transact(ctx, s.db, func(tx *database.Tx) (recovered, error) {
		run, current, err := lockStep(ctx, tx, stepID)
		if err != nil {
			return recovered{}, err
		}
		if current.Status != StatusRunning || current.Attempt != attempt || run.Status != RunRunning {
			return recovered{}, ErrStepAttemptLost
		}
		receipt, err := workflowsql.New(tx.SQL()).StepStartReceipt(ctx, workflowsql.StepStartReceiptParams{RunID: run.ID, Step: current.Name, Stage: current.Stage, Attempt: attempt})
		if err != nil {
			return recovered{}, fmt.Errorf("reading step %d's start receipt: %w", stepID, database.Missing(err))
		}
		var started stepStartedReceipt
		if err := json.Unmarshal(receipt, &started); err != nil {
			return recovered{}, err
		}
		current.ownedAttempt, current.ownedStepID, current.ownedRunID = current.Attempt, current.ID, current.RunID
		current.ownedProjectID, current.ownedSourceGeneration = run.ProjectID, started.SourceGeneration
		return recovered{current, run}, nil
	})
	if err != nil {
		return nil, err
	}
	return &Execution{service: s, ctx: s.stepContext(ctx, got.run.ProjectID), step: got.step, run: got.run, claim: got.step}, nil
}

type recovered struct {
	step *Step
	run  *Run
}

// masked is evidence with every string value passed through deps.Mask: a
// step that printed a secret records "[secret]". Evidence is rewritten only
// when something was masked; evidence that is not JSON is left as it is.
func (s *Service) masked(ctx context.Context, evidence json.RawMessage) json.RawMessage {
	if s.masker == nil || len(evidence) == 0 {
		return evidence
	}
	// Numbers stay exactly as written: decoding into float64 would change
	// a large integer elsewhere in the evidence when one string is masked.
	dec := json.NewDecoder(bytes.NewReader(evidence))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return evidence
	}
	v, changed := maskStrings(v, s.masker(ctx))
	if !changed {
		return evidence
	}
	out, err := json.Marshal(v)
	if err != nil {
		return evidence
	}
	return out
}

func maskStrings(v any, mask func(string) string) (any, bool) {
	changed := false
	switch t := v.(type) {
	case string:
		m := mask(t)
		return m, m != t
	case []any:
		for i := range t {
			var c bool
			t[i], c = maskStrings(t[i], mask)
			changed = changed || c
		}
	case map[string]any:
		for k := range t {
			var c bool
			t[k], c = maskStrings(t[k], mask)
			changed = changed || c
		}
	}
	return v, changed
}
