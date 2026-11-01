package verification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func (a *Guidance) collectGuidanceChecksFor(ctx context.Context, run *workflow.Run) ([]conformance.DeclaredCheck, *adoption.RecordedRevision, error) {
	ad, _ := adoption.ProjectAdoptionIn(ctx, a.db, run.ProjectID, run.AdoptionID)
	if ad == nil {
		return nil, nil, fmt.Errorf("run %d has no recorded adoption", run.ID)
	}
	checks, err := ChecksOfReceipt(ctx, a.adoption, ad)
	return checks, ad, err
}
func ChecksOfReceipt(ctx context.Context, adoptions *adoption.Service, ad *adoption.RecordedRevision) ([]conformance.DeclaredCheck, error) {
	var projectID ids.ID
	if ad != nil {
		projectID = ad.ProjectID
	}
	checks, err := adoptions.Checks(ctx, adoption.Source{ProjectID: projectID, Revision: ad})
	if err != nil {
		return nil, err
	}
	return declarations(checks), nil
}

// changeChecks are the checks a change is judged on outside a run: the
// revision its own run reads when it has one (§4.1, so the agent's loop and
// the gate agree), else the project's current receipt. A read that fails is
// the error, never the checks of another revision; a project with no
// receipt has adopted nothing.
func (v *Changes) changeChecks(ctx context.Context, ch *workflow.Change) ([]conformance.DeclaredCheck, *adoption.RecordedRevision, error) {
	var ad *adoption.RecordedRevision
	if !ch.RunID.IsZero() {
		run, err := workflow.ReadRunIn(ctx, v.db, ch.RunID)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the change's run: %w", err)
		}
		if ad, err = adoption.ProjectAdoptionIn(ctx, v.db, run.ProjectID, run.AdoptionID); err != nil {
			return nil, nil, fmt.Errorf("reading the adoption run %s recorded: %w", run.ID, err)
		}
	} else {
		current, err := adoption.CurrentReceiptIn(ctx, v.db, ch.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			// No receipt: only a project at revision zero has adopted
			// nothing; a revision whose receipt is missing is unknown law.
			at, err := adoption.CurrentIn(ctx, v.db, ch.ProjectID)
			if err != nil {
				return nil, nil, fmt.Errorf("reading the project's adoption: %w", err)
			}
			if at.Revision != 0 {
				return nil, nil, fmt.Errorf("project %s: revision %d: %w", ch.ProjectID, at.Revision, adoption.ErrRevisionUnavailable)
			}
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading the project's adoption: %w", err)
		}
		ad = current
	}
	checks, err := ChecksOfReceipt(ctx, v.adoption, ad)
	return checks, ad, err
}
