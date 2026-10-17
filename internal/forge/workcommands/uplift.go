package workcommands

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/workflow"
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

type Uplift struct{ Changes workflow.Changes }

func (a Uplift) OpenUplift(ctx context.Context, in adoption.UpliftRequest) (adoption.UpliftChange, error) {
	if in.Target == nil {
		return adoption.UpliftChange{}, fmt.Errorf("adoption target recorder is unavailable")
	}
	c := in.Candidate
	ch, err := a.Changes.Uplift(ctx, workflow.Uplift{ProjectID: c.ProjectID, ExpectedRevision: c.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: c.Digest, Checks: workAcceptanceChecks(c.NewChecks)}, Words: c.Words(), Target: upliftTarget{service: in.Target, candidate: c}, Basis: in.Basis})
	if err != nil {
		return adoption.UpliftChange{}, err
	}
	return adoption.UpliftChange{ID: ch.ID, Number: ch.Number}, nil
}

func workAcceptanceChecks(checks []adoption.CheckSummary) []workflow.AcceptanceCheck {
	out := make([]workflow.AcceptanceCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, workflow.AcceptanceCheck{Block: c.Block, Check: c.Check, Version: c.Version, Title: c.Title, Expects: c.Expects})
	}
	return out
}
