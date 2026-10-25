package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

var errAssertionChanged = fault.New(fault.Conflict, "assertion changed or is outside the active run stage")

type Assertion struct{ StepID ids.ID }

type assertionOutcome struct {
	state    StepState
	evidence json.RawMessage
}

type MergeObservation struct {
	Merged           bool
	Conflicts, Drift []string
	Refusal          string
	Blocking         *Step
}

// MergeAssertion is the exact detached observation whose SCM effect is requested.
type MergeAssertion struct {
	Run    Run
	Step   Step
	Source MergeSource
}

type MergeObserver interface {
	ObserveMerge(context.Context, MergeAssertion) (MergeObservation, error)
}

// Assertions owns check evaluation and persistence. Database facts are
// read in the committing transaction. SCM runs outside it, and its prepared scope
// must still match before its result and receipt can be committed.
type Assertions struct {
	db    *database.DB
	merge MergeObserver
	clocked
}

type assertionScope struct {
	run    *Run
	step   *Step
	source MergeSource
}

func (s Assertions) Evaluate(ctx context.Context, request Assertion) (*Step, error) {
	return s.evaluate(ctx, request, nil)
}

func (s Assertions) evaluate(ctx context.Context, request Assertion, expected *assertionScope) (*Step, error) {
	prepared, err := database.Transact(ctx, s.db, func(tx *database.Tx) (assertionScope, error) {
		run, step, err := lockStep(ctx, tx, request.StepID)
		if err != nil {
			return assertionScope{}, err
		}
		if err := assertionEligible(run, step); err != nil {
			return assertionScope{}, err
		}
		if expected != nil && !expected.matches(run, step) {
			return assertionScope{}, errAssertionChanged
		}
		if err := requireRunReady(ctx, tx, run.ID); err != nil {
			return assertionScope{}, err
		}
		scope := assertionScope{run: run, step: step}
		if step.Type == "scm/merged" && step.OverrideBy == "" {
			scope.source, err = readMergeSourceForRun(ctx, tx, run)
			if err != nil {
				return assertionScope{}, err
			}
		}
		return scope, nil
	})
	if err != nil {
		return nil, err
	}
	if prepared.step.OverrideBy != "" {
		return prepared.step, nil
	}
	var merged *MergeObservation
	if prepared.step.Type == "scm/merged" {
		if s.merge == nil {
			return nil, fmt.Errorf("merge evaluation is unavailable")
		}
		observation, err := s.merge.ObserveMerge(ctx, prepared.call())
		if err != nil {
			return nil, err
		}
		merged = &observation
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Step, error) {
		run, step, err := lockStep(ctx, tx, request.StepID)
		if err != nil {
			return nil, err
		}
		if err := assertionEligible(run, step); err != nil {
			return nil, err
		}
		if !prepared.matches(run, step) {
			return nil, errAssertionChanged
		}
		if step.Type == "scm/merged" {
			source, err := readMergeSourceForRun(ctx, tx, run)
			if err != nil {
				return nil, err
			}
			if source != prepared.source {
				return nil, errAssertionChanged
			}
		}
		outcome, err := evaluateAssertionIn(ctx, tx, run, step, merged)
		if err != nil {
			return nil, err
		}
		if outcome.state != StatusPass && step.Status == StatusPending && step.Type != "scm/merged" {
			waiting, err := awaitingStageWork(ctx, tx, step)
			if err != nil {
				return nil, err
			}
			if waiting {
				return step, nil
			}
		}
		next, err := step.Status.Transition(MoveStepEvaluate, outcome.state)
		if err != nil {
			return nil, err
		}
		n, err := workflowsql.New(tx.SQL()).RecordStepEvaluation(ctx, workflowsql.RecordStepEvaluationParams{At: s.now(), ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String(), Status: next.String(), EvidenceJson: outcome.evidence})
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, errAssertionChanged
		}
		if next != step.Status {
			receipt := stepEvaluationReceipt{step.Name, next}
			if err := recordReceiptIn(ctx, tx, run.ID, "check.evaluated", receipt, s.now()); err != nil {
				return nil, err
			}
		}
		return stepResult(workflowsql.New(tx.SQL()).StepByID(ctx, step.ID))
	})
}

// awaitingStageWork reports whether a step of the assertion's stage that does
// work, rather than approve or assert, has yet to finish. That work may still
// attach the artifact or decision the assertion asks for (a deploy attaches the
// release), so a fact missing now is not yet a failure: the assertion stays
// pending, and each finished step refreshes it. A merge check is excluded
// because its observation is an SCM effect whose result must be recorded.
func awaitingStageWork(ctx context.Context, tx *database.Tx, assertion *Step) (bool, error) {
	unfinished, err := unfinishedStageWork(ctx, tx, assertion)
	return len(unfinished) > 0, err
}

// unfinishedStageWork names the steps of step's stage that do work and have
// yet to finish: pending or running. Assertions wait for them, an approval
// may not be given before them, and activity reads them as AppTown
// working.
func unfinishedStageWork(ctx context.Context, tx *database.Tx, step *Step) ([]string, error) {
	steps, err := readStageSteps(ctx, tx, step.RunID, step.Stage)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range steps {
		if doesStageWork(s.Type) && (s.Status == StatusPending || s.Status == StatusRunning) {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// doesStageWork says whether a step kind does the stage's work: everything
// but an approval (a person's decision) and an assertion (a check of facts
// the work leaves).
func doesStageWork(kind string) bool { return kind != "approval" && !assertionKind(kind) }

func assertionEligible(run *Run, step *Step) error {
	if step == nil || run == nil || step.RunID != run.ID || run.Status != RunRunning || run.CurrentStage != step.Stage {
		return errAssertionChanged
	}
	if !step.isAssertion() {
		return fmt.Errorf("step %q is not an assertion", step.Name)
	}
	if step.OverrideBy != "" {
		return nil
	}
	if _, err := step.Status.Transition(MoveStepEvaluate, StatusPass); err != nil {
		return fmt.Errorf("%w: %w", errAssertionChanged, err)
	}
	return nil
}

func (s *Step) isAssertion() bool { return assertionKind(s.Type) }

func assertionKind(kind string) bool {
	return kind == "artifact/require" || kind == "adr/require" || kind == "scm/merged"
}

func (p assertionScope) matches(run *Run, step *Step) bool {
	return *p.run == *run && SameStep(p.step, step)
}

func assertionResult(state StepState, evidence any) (assertionOutcome, error) {
	body, err := json.Marshal(evidence)
	return assertionOutcome{state, body}, err
}

func evaluateAssertionIn(ctx context.Context, tx *database.Tx, run *Run, step *Step, merged *MergeObservation) (assertionOutcome, error) {
	q := workflowsql.New(tx.SQL())
	switch step.Type {
	case "artifact/require":
		var cfg artifactRequirement
		if err := json.Unmarshal(step.Config, &cfg); err != nil {
			return assertionOutcome{}, fmt.Errorf("invalid artifact assertion: %w", err)
		}
		artifact, err := q.AssertionArtifact(ctx, workflowsql.AssertionArtifactParams{RunID: run.ID, Kind: cfg.Kind, Name: cfg.Name})
		if errors.Is(err, sql.ErrNoRows) {
			return assertionResult(StatusFail, struct {
				Reason string `json:"reason"`
			}{fmt.Sprintf("no artifact of kind %q attached to this run", cfg.Kind)})
		}
		if err != nil {
			return assertionOutcome{}, err
		}
		return assertionResult(StatusPass, struct {
			ArtifactID ids.ID `json:"artifactId"`
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			Ref        string `json:"ref"`
		}{artifact.ID, artifact.Kind, artifact.Name, artifact.Ref})
	case "adr/require":
		var cfg decisionRequirement
		if err := json.Unmarshal(step.Config, &cfg); err != nil {
			return assertionOutcome{}, fmt.Errorf("invalid ADR assertion: %w", err)
		}
		if cfg.Status == "" {
			cfg.Status = adrs.Accepted.String()
		}
		if run.ChangeID.IsZero() {
			return assertionResult(StatusFail, struct {
				Reason string `json:"reason"`
			}{"run has no change; adr/require needs one"})
		}
		status, err := adrs.ParseState(cfg.Status)
		if err != nil {
			return assertionOutcome{}, err
		}
		matches, err := adrs.MatchingIn(ctx, tx, adrs.MatchRequest{ProjectID: run.ProjectID, ChangeID: run.ChangeID, State: status, Tags: cfg.Tags})
		if err != nil {
			return assertionOutcome{}, err
		}
		if len(matches) > 0 {
			return assertionResult(StatusPass, struct {
				ADRs           []adrs.Match `json:"adrs"`
				RequiredStatus string       `json:"requiredStatus"`
			}{matches, cfg.Status})
		}
		return assertionResult(StatusFail, struct {
			Reason         string   `json:"reason"`
			RequiredStatus string   `json:"requiredStatus"`
			RequiredTags   []string `json:"requiredTags"`
		}{fmt.Sprintf("no ADR with status %q linked to this change", cfg.Status), cfg.Status, cfg.Tags})
	case "scm/merged":
		if merged == nil {
			return assertionOutcome{}, fmt.Errorf("missing merge observation")
		}
		if merged.Refusal != "" {
			return assertionResult(StatusNeedsHuman, MergeEvidence{Reason: merged.Refusal})
		}
		if merged.Merged {
			return assertionResult(StatusPass, mergedEvidence{true})
		}
		return assertionResult(StatusNeedsHuman, MergeBlockEvidence(merged.Conflicts, merged.Drift))
	default:
		return assertionOutcome{}, fmt.Errorf("step %q is not an assertion", step.Name)
	}
}

func NewAssertions(db *database.DB, merge MergeObserver) *Assertions {
	return &Assertions{db: db, merge: merge}
}

func (p assertionScope) call() MergeAssertion {
	step := *p.step
	step.Config = append(step.Config[:0:0], step.Config...)
	step.Evidence = append(step.Evidence[:0:0], step.Evidence...)
	return MergeAssertion{Run: *p.run, Step: step, Source: p.source}
}

func readMergeSourceForRun(ctx context.Context, db database.Queryer, run *Run) (MergeSource, error) {
	return ReadMergeSourceIn(ctx, db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
}

type stepEvaluationReceipt struct {
	Step   string    `json:"step"`
	Status StepState `json:"status"`
}

// Evaluate re-checks an assertion step, an artifact, an ADR or a merge, and
// records what it found.
func (s *Service) Evaluate(ctx context.Context, stepID ids.ID) (*Step, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	source, err := readStep(ctx, s.db, stepID)
	if err != nil {
		return nil, err
	}
	run, err := readRun(ctx, s.db, source.RunID)
	if err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, operationPermission{Action: actionRunStep, ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: stepID}); err != nil {
		return nil, err
	}
	step, err := s.assertions.evaluate(ctx, Assertion{StepID: stepID}, &assertionScope{run: run, step: source})
	if err != nil {
		return nil, err
	}
	s.advanceCommitted(RunRef{ID: step.RunID, ProjectID: run.ProjectID})
	return step, nil
}

// RefreshAssertions rechecks the current stage after its evidence changes, then
// redispatches any stale checks whose reset committed. Independent assertions
// still run when one fails; all failures are returned for the caller to report.
func (s *Service) RefreshAssertions(ctx context.Context, runID ids.ID) error {
	project, _, err := s.runScope(ctx, runID, actionRunStep)
	if err != nil {
		return err
	}
	return s.refreshAssertions(ctx, RunRef{ID: runID, ProjectID: project})
}

func (s *Service) refreshAssertions(ctx context.Context, ref RunRef) error {
	runID := ref.ID
	run, err := readScopedRun(ctx, s.db, runID, ref.ProjectID)
	if err != nil || run.Status != RunRunning {
		return err
	}
	steps, err := readStageSteps(ctx, s.db, runID, run.CurrentStage)
	if err != nil {
		return err
	}
	var failures []error
	for _, step := range steps {
		if step.isAssertion() && step.Status != StatusPass && step.OverrideBy == "" {
			if _, err := s.assertions.Evaluate(ctx, Assertion{StepID: step.ID}); err != nil {
				failures = append(failures, fmt.Errorf("assertion %s: %w", step.Name, err))
			}
		}
	}
	changed, err := (reworkStore{s.db, s.clocked}).refresh(ctx, runID)
	if err == nil && changed {
		s.DispatchCommittedRetry(runID)
	}
	return errors.Join(append(failures, err)...)
}

// artifactRequirement is an artifact/require step's options.
type artifactRequirement struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// decisionRequirement is an adr/require step's options.
type decisionRequirement struct {
	Status string   `json:"status"`
	Tags   []string `json:"tags"`
}
