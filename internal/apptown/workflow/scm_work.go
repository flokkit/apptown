package workflow

import (
	"context"
	"reflect"
	"slices"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

type scmClaimKey struct{}
type scmInspectionKey struct{}
type scmInspection struct{ db *database.DB }

type SCMWork struct {
	rules   LawRules
	db      *database.DB
	dataDir string

	evals EvalGate
	clocked
}
type scmClaim struct {
	rules    LawRules
	db       *database.DB
	run      Run
	source   MergeSource
	selected scm.Source
	call     *MergeCall

	evals EvalGate
	clocked
}

func SCMInspectionContext(ctx context.Context, db *database.DB) context.Context {
	return context.WithValue(ctx, scmInspectionKey{}, scmInspection{db})
}
func (w SCMWork) RequireInspection(ctx context.Context) error {
	if scope, ok := ctx.Value(scmInspectionKey{}).(scmInspection); !ok || scope.db != w.db {
		return scm.ErrCaller
	}
	return ctx.Err()
}
func (w SCMWork) Prepare(ctx context.Context, purpose scm.Purpose, id ids.ID) (scm.Claim, error) {
	if claim, ok := ctx.Value(scmClaimKey{}).(*scmClaim); ok {
		if claim.db != w.db || claim.run.ID != id || claim.call == nil {
			return nil, scm.ErrCaller
		}
		return claim, nil
	}
	if purpose != scm.Observe {
		return nil, scm.ErrCaller
	}
	if err := w.RequireInspection(ctx); err != nil {
		return nil, err
	}
	run, err := runRecord(workflowsql.New(w.db.SQL()).RunByID(ctx, workflowsql.RunByIDParams{ID: id}))
	if err != nil {
		return nil, err
	}
	source, err := readMergeSourceForRun(ctx, w.db, run)
	if err != nil {
		return nil, err
	}
	return w.claim(*run, source, nil, nil)
}

// claim binds one operation to its admitted snapshot. attribution is what
// the admission read; nil for an inspection, which merges nothing.
func (w SCMWork) claim(run Run, source MergeSource, call *MergeCall, attribution *scm.Attribution) (*scmClaim, error) {
	if call != nil {
		snapshot := *call
		snapshot.Step.Config = slices.Clone(call.Step.Config)
		snapshot.Step.Evidence = slices.Clone(call.Step.Evidence)
		call = &snapshot
	}
	selected := scm.Source{RunID: source.RunID, RunNumber: run.Number, ProjectID: source.ProjectID, Branch: source.Branch, WorkflowName: source.WorkflowName, WorkflowVersion: source.WorkflowVersion, MR: scm.MergeRequest{Project: source.MR.Project, IID: source.MR.IID},
		Candidate: source.Landing.Candidate, Target: source.Landing.Target, PinRequired: source.PinRequired, Attribution: attribution}
	if source.RepoBacked() {
		var err error
		selected.Repository, err = source.Repository(w.dataDir)
		if err != nil {
			return nil, err
		}
		selected.Workspace, err = source.Workspace(w.dataDir)
		if err != nil {
			return nil, err
		}
		selected.DefaultBranch = source.DefaultBranch
	}
	return &scmClaim{w.rules, w.db, run, source, selected, call, w.evals, w.clocked}, nil
}
func (c *scmClaim) Source() scm.Source { return c.selected }
func (c *scmClaim) CheckIn(ctx context.Context, tx *database.Tx, purpose scm.Purpose) (scm.Refusal, error) {
	run, err := lockRunSnapshotIn(ctx, tx, &c.run)
	if err != nil {
		return scm.Refusal{}, err
	}
	source, err := readMergeSourceForRun(ctx, tx, run)
	if err != nil {
		return scm.Refusal{}, err
	}
	if source != c.source {
		return scm.Refusal{}, scm.ErrChanged
	}
	switch purpose {
	case scm.Observe:
		return scm.Refusal{}, nil
	case scm.NewEffect:
		if c.call == nil {
			return scm.Refusal{}, scm.ErrCaller
		}
		if err := run.Status.RequireRunning(); err != nil {
			return scm.Refusal{}, err
		}
		if c.source.PinRequired && c.source.Landing.Candidate == "" {
			return scm.Refusal{Reason: ErrLandingUnpinned.Error()}, nil
		}
		if c.source.Landing.Candidate != "" {
			reason, err := landingCandidateRefusal(ctx, tx, run.ID, c.source.Landing)
			if err != nil || reason != "" {
				return scm.Refusal{Reason: reason}, err
			}
		}
		permission, err := (MergeAdmission{c.db, c.rules, c.evals, c.clocked}).evaluateMergePermissionIn(ctx, tx, *c.call)
		refusal := scm.Refusal{Reason: permission.Reason}
		if permission.Blocking != nil {
			refusal.CheckID = permission.Blocking.ID
		}
		// What GitLab and the merge commit will say must be what this
		// check-in admitted: an approval withdrawn, an override or an edit
		// since the claim was made sends the attempt back to be admitted
		// again.
		if err == nil && permission.Admitted != nil && !reflect.DeepEqual(permission.Admitted.attribution, c.selected.Attribution) {
			return scm.Refusal{}, scm.ErrChanged
		}
		return refusal, err
	default:
		return scm.Refusal{}, scm.ErrCaller
	}
}
func (c *scmClaim) RecordIn(ctx context.Context, tx *database.Tx, receipt scm.Receipt) error {
	event, err := receipt.Event()
	if err != nil {
		return err
	}
	if receipt.Kind == scm.Completed && !c.source.ChangeID.IsZero() {
		count, err := workflowsql.New(tx.SQL()).SetChangeLandedSHA(ctx, workflowsql.SetChangeLandedSHAParams{LandedSha: receipt.SHA, ChangeID: c.source.ChangeID, ProjectID: c.source.ProjectID})
		if err != nil {
			return err
		}
		if count != 1 {
			return scm.ErrChanged
		}
	}
	return recordReceiptIn(ctx, tx, c.run.ID, event, receipt, c.now())
}

func NewSCMWork(db *database.DB, dataDir string, rules LawRules) SCMWork {
	return SCMWork{db: db, dataDir: dataDir, rules: rules}
}
