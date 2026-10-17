package forge

import (
	"context"
	"log"

	"flokkit.io/forge/internal/forge/identity"
)

// Object capabilities (design/subsystems/authorization-capabilities.md §11)
// live in internal/access: the Change git ticket that lets a local
// checkout push exactly one Change ref from exactly the tip it last saw.
// Possessing one never widens the person's authority; the rule decided
// that when the ticket was minted, and the receive path re-checks the
// object's state. These are the doors.

// gitTicketByToken resolves a presented ticket that is live.
func (a *App) gitTicketByToken(ctx context.Context, tok string) *identity.Ticket {
	t, err := a.Access.TicketByToken(ctx, tok)
	readOK(err)
	return t
}

// sweepExpiredCredentials drops what can never be presented again. Runs
// from the scheduler; cheap.
func (a *App) sweepExpiredCredentials(ctx context.Context) {
	if err := a.Access.Sweep(ctx); err != nil {
		log.Printf("sweeping expired credentials: %v", err)
	}
}
