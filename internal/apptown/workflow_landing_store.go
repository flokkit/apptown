package apptown

import (
	"context"
	"database/sql"

	"github.com/flokkit/apptown/internal/apptown/adoption"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Adoption receipts are immutable. Both reads use the supplied transaction and
// the owner validates their records; Work receives only the scoped revision pair.
type landingAdoptions struct{}

func (landingAdoptions) RevisionsIn(ctx context.Context, q database.Queryer, in workflow.AdoptionPairRequest) (workflow.AdoptionPair, error) {
	if err := ctx.Err(); err != nil {
		return workflow.AdoptionPair{}, err
	}
	from, err := adoption.ByIDIn(ctx, q, in.FromID)
	if err != nil {
		return workflow.AdoptionPair{}, err
	}
	if from.ProjectID != in.ProjectID {
		return workflow.AdoptionPair{}, sql.ErrNoRows
	}
	if err := ctx.Err(); err != nil {
		return workflow.AdoptionPair{}, err
	}
	to, err := adoption.ByIDIn(ctx, q, in.ToID)
	if err != nil {
		return workflow.AdoptionPair{}, err
	}
	if to.ProjectID != in.ProjectID {
		return workflow.AdoptionPair{}, sql.ErrNoRows
	}
	if err := ctx.Err(); err != nil {
		return workflow.AdoptionPair{}, err
	}
	return workflow.AdoptionPair{From: from.Revision, To: to.Revision}, nil
}
