package workcommands

import (
	"context"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// These adapters translate Work's guarded Change admission into Adoption's
// target participant. Work owns the transaction; Adoption owns its receipt.
type upliftTarget struct {
	service   adoption.TargetRecorder
	candidate adoption.Candidate
}

func (t upliftTarget) RecordTargetIn(ctx context.Context, tx *database.Tx, in workflow.TargetChange) (workflow.RecordedTarget, error) {
	result, err := t.service.RecordTargetIn(ctx, tx, adoption.Target{ProjectID: in.ProjectID, ChangeID: in.ChangeID, Candidate: t.candidate, Actor: in.Actor})
	return workflow.RecordedTarget{ID: result.ID, Candidate: result.Candidate}, err
}

// RunStarter starts a workflow run on an existing Change (Work's StartRun).
type RunStarter interface {
	StartRun(ctx context.Context, in workflow.StartChangeRun) (*workflow.Run, error)
}

// Uplift opens an adoption Change and starts its working run. An adoption
// through a Change means its working step makes the new checks hold, or
// passes empty when they already do (ways-of-working-versions.md), so
// opening one without a run left it waiting for someone to pick a workflow.
// The lane is the one remediation works fixes on: quick-change, else
// implement. Lanes and Runs are late-bound: the composition root builds
// them after Adoption.
type Uplift struct {
	Changes workflow.Changes
	DB      *database.DB
	Lanes   func() remediation.WorkflowResolver
	Runs    func() RunStarter
}

func (a Uplift) OpenUplift(ctx context.Context, in adoption.UpliftRequest) (adoption.UpliftChange, error) {
	if in.Target == nil {
		return adoption.UpliftChange{}, fmt.Errorf("adoption target recorder is unavailable")
	}
	c := in.Candidate
	given, err := a.inAdvance(ctx, c.ProjectID, in.InAdvance)
	if err != nil {
		return adoption.UpliftChange{}, err
	}
	ch, err := a.Changes.Uplift(ctx, workflow.Uplift{ProjectID: c.ProjectID, ExpectedRevision: c.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: c.Digest, Checks: workAcceptanceChecks(c.NewChecks)}, Words: c.Words(), Target: upliftTarget{service: in.Target, candidate: c}, Basis: in.Basis, InAdvance: given})
	if err != nil {
		return adoption.UpliftChange{}, err
	}
	out := adoption.UpliftChange{ID: ch.ID, Number: ch.Number}
	a.start(ctx, ch.ProjectID, &out)
	return out, nil
}

// errInAdvanceQuestionChanged refuses a yes in advance given to sentences
// that are not what the project's adoption would ask now.
var errInAdvanceQuestionChanged = fault.New(fault.Conflict, "what a yes in advance means on this project has changed since it was shown; read it again")

// inAdvance checks a yes in advance against what the project's adoption
// asks now: the same sentences, in order, as the person was shown, and a
// question that offers one at all. Nothing shown is no yes.
func (a Uplift) inAdvance(ctx context.Context, projectID ids.ID, shown []string) (*workflow.InAdvanceGiven, error) {
	if len(shown) == 0 {
		return nil, nil
	}
	if a.DB == nil || a.Lanes == nil {
		return nil, fmt.Errorf("AppTown cannot read what a yes in advance means here")
	}
	p, err := project.ByIDIn(ctx, a.DB, projectID)
	if err != nil {
		return nil, err
	}
	q, err := inAdvanceQuestion(ctx, a.Lanes(), &p)
	if err != nil {
		return nil, err
	}
	return givenInAdvance(p.Slug, q, shown)
}

// givenInAdvance is the yes given to shown, if shown is what q asks.
func givenInAdvance(slug string, q workflow.InAdvanceQuestion, shown []string) (*workflow.InAdvanceGiven, error) {
	if q.NotOffered != "" {
		return nil, fault.New(fault.Conflict, "no yes in advance on "+slug+": "+q.NotOffered)
	}
	if !slices.Equal(q.Shown(), shown) {
		return nil, errInAdvanceQuestionChanged
	}
	return &workflow.InAdvanceGiven{Workflow: q.Workflow, Shown: slices.Clone(shown)}, nil
}

// start begins the Change's working run as the opener. The Change is
// already admitted, so nothing here undoes it: a refusal before a run
// exists is NotStarted; a run admitted and then failed keeps the run with
// its error, since that run exists and blocks another start.
func (a Uplift) start(ctx context.Context, projectID ids.ID, out *adoption.UpliftChange) {
	if a.DB == nil || a.Lanes == nil || a.Runs == nil {
		out.NotStarted = "AppTown cannot start runs here"
		return
	}
	p, err := project.ByIDIn(ctx, a.DB, projectID)
	if err != nil {
		out.NotStarted = "reading the project: " + err.Error()
		return
	}
	lane := remediation.WorkflowFor(ctx, a.Lanes(), &p)
	if lane == "" {
		out.NotStarted = "this project has no quick-change or implement workflow to work the adoption on"
		return
	}
	out.Workflow = lane
	run, err := a.Runs().StartRun(ctx, workflow.StartChangeRun{ChangeID: out.ID, Workflow: lane})
	if run != nil {
		out.Run, out.RunNumber = run.ID, run.Number
	}
	switch {
	case err == nil:
	case run != nil:
		out.RunError = err.Error()
	default:
		out.NotStarted = err.Error()
	}
}

func workAcceptanceChecks(checks []adoption.CheckSummary) []workflow.AcceptanceCheck {
	out := make([]workflow.AcceptanceCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, workflow.AcceptanceCheck{Block: c.Block, Check: c.Check, Version: c.Version, Title: c.Title, Expects: c.Expects})
	}
	return out
}
