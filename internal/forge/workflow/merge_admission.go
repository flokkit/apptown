package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/forge/scm"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

var ErrMergeCaller = errors.New("merge requires a current workflow advancement or SCM assertion scope")

type MergePurpose uint8

const (
	MergeForAdvance MergePurpose = iota + 1
	MergeForAssertion
)

// An internal call is bound to the exact Work observation that requested it.
// The actor string used for receipts is deliberately not an authority input.
// HTTP/MCP user authorization remains at their command boundary.
type MergeCall struct {
	Purpose MergePurpose
	Run     Run
	Step    Step
	Source  MergeSource
}

func (c MergeCall) validate() error {
	if c.Run.ID.IsZero() || c.Run.ProjectID.IsZero() || c.Source.RunID != c.Run.ID || c.Source.ProjectID != c.Run.ProjectID {
		return ErrMergeCaller
	}
	switch c.Purpose {
	case MergeForAdvance:
		if !c.Step.ID.IsZero() {
			return ErrMergeCaller
		}
	case MergeForAssertion:
		if c.Step.ID.IsZero() || c.Step.RunID != c.Run.ID || c.Step.Type != "scm/merged" {
			return ErrMergeCaller
		}
	default:
		return ErrMergeCaller
	}
	return nil
}

type MergeAdmission struct {
	db        *database.DB
	rules     LawRules
	placement Placement // with a check worker, the only place gates accept
	evals     EvalGate  // an eval's rule for a merge into one of its copies; nil: none
	clocked
}

// WithEvalGate has the admission ask an eval's one rule before a merge
// into one of its copies (eval_authority.go).
func (a MergeAdmission) WithEvalGate(g EvalGate) MergeAdmission {
	a.evals = g
	return a
}

func NewMergeAdmission(db *database.DB, rules LawRules, placement Placement) MergeAdmission {
	return MergeAdmission{db: db, rules: rules, placement: placement}
}

type AdmittedMerge struct {
	db         *database.DB
	evals      EvalGate // asked again at the SCM claim that performs the merge
	rules      LawRules
	call       MergeCall
	gate       *preparedMergeGate
	completing bool
	// attribution is what admitted this merge, read in the admitting
	// transaction; SCM renders it for the merge commit and the provider.
	attribution *scm.Attribution
	clocked
}
type MergePermission struct {
	Admitted *AdmittedMerge
	Reason   string
	Blocking *Step
}

func (a MergeAdmission) Prepare(ctx context.Context, call MergeCall) (MergePermission, error) {
	call.Step.Config = slices.Clone(call.Step.Config)
	call.Step.Evidence = slices.Clone(call.Step.Evidence)
	return database.Transact(ctx, a.db, func(tx *database.Tx) (MergePermission, error) {
		return a.admitMergeIn(ctx, tx, call)
	})
}

// This is Work admission, not a receipt of an external effect. All policy and
// gate facts are read with the project/run locked; no connection escapes it.
func (a MergeAdmission) admitMergeIn(ctx context.Context, tx *database.Tx, call MergeCall) (MergePermission, error) {
	var result MergePermission
	if err := call.validate(); err != nil {
		return result, err
	}
	run, err := lockCurrentRunIn(ctx, tx, &call.Run)
	if err != nil {
		return result, err
	}
	source, err := readMergeSourceForRun(ctx, tx, run)
	if err != nil {
		return result, err
	}
	if source != call.Source {
		return result, scm.ErrChanged
	}
	return a.evaluateMergePermissionIn(ctx, tx, call)
}

// The caller holds project/run locks and has compared the complete run/source
// snapshot. Intent admission can reuse those facts instead of reading them twice.
func (a MergeAdmission) evaluateMergePermissionIn(ctx context.Context, tx *database.Tx, call MergeCall) (MergePermission, error) {
	var result MergePermission
	if err := call.validate(); err != nil {
		return result, err
	}
	run, source := &call.Run, call.Source
	spec, err := run.Definition()
	if err != nil {
		return result, err
	}
	stage, _ := spec.Stage(run.CurrentStage)
	if stage == nil {
		return result, ErrRunMoved
	}
	next, err := nextStage(spec, run.CurrentStage, !run.ParentRunID.IsZero())
	if err != nil {
		return result, err
	}
	if call.Purpose == MergeForAssertion {
		step, err := stepResult(workflowsql.New(tx.SQL()).StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: call.Step.ID}))
		if err != nil {
			return result, err
		}
		if step.Type != "scm/merged" || step.OverrideBy != "" || !SameStep(step, &call.Step) || assertionEligible(run, step) != nil {
			return result, ErrMergeCaller
		}
	}
	// Landing is an effect a person answers for (assent.go), whatever the
	// project's source.
	if err := assentEvidenceIn(ctx, tx, run.ID, run.ProjectID); errors.Is(err, ErrNoAssent) {
		return MergePermission{Reason: err.Error()}, nil
	} else if err != nil {
		return result, err
	}
	// A merge into an eval's copy happens only while its eval may proceed:
	// asked here, at the effect, whichever door led to it.
	// Read in this transaction: the rule needs no other connection while
	// this one holds the project's locks.
	if a.evals != nil {
		why, err := a.evals.RefusalIn(ctx, tx, run.ProjectID)
		if err != nil {
			return result, err
		}
		if why != "" {
			return MergePermission{Reason: why}, nil
		}
	}
	// Every check below applies to every landing. The SCM effect merges into
	// the project's recorded default branch; a project with no repository
	// has none, so its landing merges nothing, but the project's rules, the
	// stage's required checks and the adoption it was worked under still
	// decide whether it may complete (they were skipped for such projects).
	if !run.ParentRunID.IsZero() || (call.Purpose == MergeForAdvance && !next.Merge) {
		return result, ErrMergeCaller
	}
	law, err := (lawStore{a.db, a.rules, a.clocked}).evaluateIn(ctx, tx, run, false)
	if err != nil {
		return result, err
	}
	if law != nil {
		return MergePermission{Reason: "the current project rules must be satisfied before merging", Blocking: law}, nil
	}
	checks, err := readStageChecks(ctx, tx, run.ID, run.CurrentStage)
	if err != nil {
		return result, err
	}
	steps, err := stepRecords(workflowsql.New(tx.SQL()).StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: run.ID, Stage: run.CurrentStage}))
	if err != nil {
		return result, err
	}
	required, err := evaluateGate(stage, checks, steps, a.placement)
	if err != nil {
		return result, err
	}
	if !required.PermitsMerge() {
		pending := slices.Clone(required.Missing)
		for _, step := range required.Blocking {
			if step.Type != "scm/merged" {
				pending = append(pending, step.Name)
				if result.Blocking == nil {
					result.Blocking = step
				}
			}
		}
		result.Reason = fmt.Sprintf("waiting for required checks before merging: %s", strings.Join(pending, ", "))
		return result, nil
	}

	if reason := source.UpliftRefusal(); reason != "" {
		return MergePermission{Reason: reason}, nil
	}
	gate, err := prepareMergeGateIn(ctx, tx, a.db, run, source, a.clocked)
	if err != nil {
		return result, err
	}
	attribution, err := landingAttributionIn(ctx, tx, run, source)
	if err != nil {
		return result, err
	}
	result.Admitted = &AdmittedMerge{db: a.db, evals: a.evals, rules: a.rules, call: call, gate: gate, completing: next.Completing, attribution: attribution, clocked: a.clocked}
	return result, nil
}

// Context carries only this admitted Work snapshot. SCM revalidates it before effect.
func (a *AdmittedMerge) Context(ctx context.Context, dataDir string) (context.Context, error) {
	if a == nil || a.gate == nil || a.db == nil {
		return nil, ErrMergeCaller
	}
	claim := (SCMWork{db: a.db, dataDir: dataDir, rules: a.rules, evals: a.evals, clocked: a.clocked}).claim(a.call.Run, a.call.Source, &a.call, a.attribution)
	return context.WithValue(ctx, scmClaimKey{}, claim), nil
}
