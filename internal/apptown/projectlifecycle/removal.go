package projectlifecycle

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type projectRemoval struct {
	workflow.ProjectRemoval
	ReleasePreviews *ReleasePreviewInventory
}

// removeRecords composes the owners, never their table names. Work locks
// admission first; the root and its required receipt settle last.
func (s *Service) removeRecords(ctx context.Context, projectID ids.ID, who string, record func(*database.Tx, projectRemoval) error) (projectRemoval, error) {
	var removal projectRemoval
	if who == "" {
		return removal, fmt.Errorf("project deletion requires a receipt actor")
	}
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		removal.ProjectRemoval, err = workflow.ProjectRemovalIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		owner, err := project.ByIDIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		numbers, err := deployment.ReleaseNumbersIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		removal.ReleasePreviews = &ReleasePreviewInventory{Version: 1, ProjectID: projectID, ServingLabel: owner.ServingLabel, Numbers: numbers}
		for _, participant := range []struct {
			name   string
			remove func(context.Context, *database.Tx, ids.ID) error
		}{
			{"Work", workflow.RemoveProjectIn},
			{"Delivery", deployment.RemoveProjectIn},
			{"Adoption", adoption.RemoveProjectIn},
			{"Integration", integration.RemoveProjectIn},
			{"Conformance", conformance.RemoveProjectIn},
			{"Access", identity.RemoveProjectIn},
			{"ADRs", adrs.RemoveProjectIn},
			{"Secrets", secret.RemoveProjectIn},
		} {
			if err := participant.remove(ctx, tx, projectID); err != nil {
				return fmt.Errorf("%s: %w", participant.name, err)
			}
		}
		if err := project.RemoveIn(ctx, tx, projectID, who, map[string]any{
			"runs": removal.Counts.Runs, "changes": removal.Counts.Changes, "steps": removal.Counts.Steps,
			"events": removal.Counts.Events, "artifacts": removal.Counts.Artifacts,
			"cleanup": "pending", "note": "usage, retained backups and ledger history left intact",
		}, s.now()); err != nil {
			return err
		}
		return record(tx, removal)
	})
	if err != nil {
		return projectRemoval{}, err
	}
	return removal, nil
}
