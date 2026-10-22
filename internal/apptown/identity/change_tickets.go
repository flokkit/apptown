package identity

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// RevokeChangeTicketsIn closes outstanding Git tickets with the Change's
// abandonment. The caller owns the Change transaction and its cleanup receipt.
func RevokeChangeTicketsIn(ctx context.Context, tx *database.Tx, changeID ids.ID, at time.Time) error {
	_, err := queries(tx).RevokeChangeTickets(ctx, accesssql.RevokeChangeTicketsParams{RevokedAt: at.UTC(), Kind: TicketKindGit, ResourceID: changeID.String()})
	return err
}
