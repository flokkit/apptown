package project

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ClaimAddressIn assigns an address permanently, or confirms this project's
// existing assignment. A historical reservation with no known owner is taken.
func ClaimAddressIn(ctx context.Context, tx *database.Tx, id ids.ID, address string) error {
	if id.IsZero() || !ValidSlug(address) {
		return ErrSlug
	}
	n, err := queries(tx).ClaimProjectAddress(ctx, projectsql.ClaimProjectAddressParams{Address: address, ProjectID: id})
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	owner, err := queries(tx).ProjectAddressOwner(ctx, address)
	if err != nil {
		return err
	}
	if owner != id {
		return ErrSlugTaken
	}
	return nil
}

// IDByAddressIn resolves a live or retained project through the same permanent
// namespace as BySlug. A purged reservation never resolves to a project.
func IDByAddressIn(ctx context.Context, q database.Queryer, address string) (ids.ID, error) {
	id, err := queries(q).ProjectIDByAddress(ctx, address)
	if err != nil {
		return ids.Nil, database.Missing(database.QueryError(err, "ProjectIDByAddress"))
	}
	return id, nil
}
