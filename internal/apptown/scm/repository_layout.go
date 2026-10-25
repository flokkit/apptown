package scm

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// PublicationLocation is active recovery state, not a historical path label.
// Its immutable tuple is checked again when relocation updates its location.
type PublicationLocation struct {
	ID         ids.ID `json:"id"`
	ProjectID  ids.ID `json:"projectId"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Expected   string `json:"expected"`
	Proposed   string `json:"proposed"`
}

func PublicationLocationsIn(ctx context.Context, q database.Queryer) ([]PublicationLocation, error) {
	rows, err := scmsql.New(q.SQL()).UnsettledPublications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PublicationLocation, 0, len(rows))
	for _, row := range rows {
		out = append(out, PublicationLocation{ID: row.ID, ProjectID: row.ProjectID, Repository: row.Repository, Branch: row.Branch, Expected: row.Expected, Proposed: row.Proposed})
	}
	return out, nil
}

// RelocateIn changes only the path. A changed owner or publication tuple refuses;
// replay after a committed transaction accepts exactly the intended destination.
func (p PublicationLocation) RelocateIn(ctx context.Context, tx *database.Tx, destination string) error {
	if p.ID.IsZero() || p.ProjectID.IsZero() || p.Repository == "" || destination == "" {
		return fmt.Errorf("publication relocation needs identified source and destination")
	}
	n, err := scmsql.New(tx.SQL()).RelocatePublication(ctx, scmsql.RelocatePublicationParams{ID: p.ID, ProjectID: p.ProjectID, Branch: p.Branch, Expected: p.Expected, Proposed: p.Proposed, Source: p.Repository, Destination: destination})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("publication %s changed during repository relocation", p.ID)
	}
	return nil
}
