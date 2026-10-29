package project

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// AddressFreeIn reports whether project id may take address: ErrSlug for
// one of the wrong shape, ErrSlugTaken while another project has it as its
// address or as its serving label (the name its apps were first served
// under, which stays with it until purge), or while a purged project's
// unfinished cleanup still names it (its previews and caches are found by
// that name). An address a project has left is free.
func AddressFreeIn(ctx context.Context, q database.Queryer, id ids.ID, address string) error {
	if !ValidSlug(address) {
		return ErrSlug
	}
	taken, err := queries(q).ProjectAddressTaken(ctx, projectsql.ProjectAddressTakenParams{Address: address, ID: id})
	if err != nil {
		return database.QueryError(err, "ProjectAddressTaken")
	}
	if taken {
		return ErrSlugTaken
	}
	return nil
}

// IDBySlugIn resolves a live or retained project by its current address. An
// address a project no longer has names nothing.
func IDBySlugIn(ctx context.Context, q database.Queryer, slug string) (ids.ID, error) {
	id, err := queries(q).ProjectIDBySlug(ctx, slug)
	if err != nil {
		return ids.Nil, database.Missing(database.QueryError(err, "ProjectIDBySlug"))
	}
	return id, nil
}
