package identity

import (
	"context"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
)

// RevokeChangeTicketsIn closes outstanding Git tickets with the Change's
// abandonment. The caller owns the Change transaction and its cleanup receipt.
func RevokeChangeTicketsIn(ctx context.Context, tx *database.Tx, changeID ids.ID, at time.Time) error {
	_, err := queries(tx).RevokeChangeTickets(ctx, accesssql.RevokeChangeTicketsParams{RevokedAt: at.UTC(), Kind: TicketKindGit, ResourceID: changeID.String()})
	return err
}
