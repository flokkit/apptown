package verification

import (
	"context"
	"fmt"
	"log"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
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
	return Declarations(checks), nil
}
func changeAdoption(ctx context.Context, db database.Queryer, ch *workflow.Change) *adoption.RecordedRevision {
	if ch != nil && !ch.RunID.IsZero() {
		if run, err := workflow.ReadRunIn(ctx, db, ch.RunID); err == nil {
			ad, _ := adoption.ProjectAdoptionIn(ctx, db, run.ProjectID, run.AdoptionID)
			return ad
		}
	}
	if ch == nil {
		return nil
	}
	ad, err := adoption.CurrentReceiptIn(ctx, db, ch.ProjectID)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	return ad
}
