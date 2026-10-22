package identity

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// TicketHoldsIn reports whether a change ticket can still be used: it
// exists, is not revoked, and has not expired. A ticket used for a push
// still holds its change until the push is synced; the sync releases it.
func TicketHoldsIn(ctx context.Context, q database.Queryer, id ids.ID, now time.Time) (bool, error) {
	row, err := queries(q).TicketByID(ctx, accesssql.TicketByIDParams{ID: id, Kind: TicketKindGit})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, database.QueryError(err, "reading a change ticket")
	}
	return !row.RevokedAt.Valid && row.ExpiresAt.After(now), nil
}

// TicketPersonIn is the principal of the person a git ticket was minted
// for; Nil when the ticket is not found or its person was removed.
func TicketPersonIn(ctx context.Context, q database.Queryer, id ids.ID) (ids.ID, error) {
	row, err := queries(q).TicketByID(ctx, accesssql.TicketByIDParams{ID: id, Kind: TicketKindGit})
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, nil
	}
	if err != nil {
		return ids.Nil, database.QueryError(err, "reading a change ticket")
	}
	return row.PrincipalID, nil
}
