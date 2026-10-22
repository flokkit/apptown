package identity

import (
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/stamp"
)

// Followup describes work required by an account change in its transaction.
type Followup struct{ EndSessions bool }

// SetPassword retains an existing password when no replacement was supplied.
func (a *Account) SetPassword(hash string) Followup {
	if hash == "" {
		return Followup{}
	}
	a.passwordHash, a.HasPassword = hash, true
	return Followup{EndSessions: true}
}

// access.Event is the receipt of a value's transition; persistence belongs to Service.
var (
	ErrLeaked          = errors.New("this refresh token was already rotated; the connection has been ended for safety")
	ErrUnknownRefresh  = errors.New("unknown refresh token")
	ErrConnectionEnded = errors.New("the connection has ended; connect the agent again")
	ErrClientMismatch  = errors.New("client mismatch")
	ErrTicketConsumed  = errors.New("ticket already consumed")
	ErrTicketRevoked   = errors.New("ticket revoked")
)

// refreshCredential is the one presented credential, loaded under its row lock.
// Rotation needs no unbounded history of a long-lived connection's tokens.
type refreshCredential struct {
	hash    string
	expires time.Time
	revoked stamp.Time
}

// Bounds narrows a human identity to this connection's consent. Applying a
// second bound intersects scope words and never shares a mutable scope slice.
// A delegation that has ended by now, AppTown's clock, bounds to nothing.
func (d Delegation) Bounds(pr access.Principal, now time.Time) access.Principal {
	if !d.LiveAt(now) || pr.Kind != "user" || pr.Name != d.User {
		return access.Principal{}
	}
	scopes := append([]string(nil), d.Scopes...)
	if pr.Via == "oauth" || !pr.DelegationID.IsZero() {
		scopes = nil
		for _, scope := range d.Scopes {
			for _, held := range pr.Scopes {
				if scope == held {
					scopes = append(scopes, scope)
					break
				}
			}
		}
	}
	pr.Via, pr.DelegationID, pr.Scopes = "oauth", d.ID, scopes
	return pr
}

func (d *Delegation) end(now time.Time) {
	if !d.RevokedAt.Valid {
		d.RevokedAt = stamp.Of(now)
	}
}

// Rotate consumes the selected refresh credential. Reuse takes precedence over
// expiry: a spent token still proves a leak after its nominal lifetime. now
// is AppTown's clock, which judges the token's expiry and stamps its use.
func (d *Delegation) Rotate(presented string, now time.Time) (Pair, error) {
	if d.refresh.hash == "" || HashToken(presented) != d.refresh.hash {
		return Pair{}, ErrUnknownRefresh
	}
	if d.refresh.revoked.Valid {
		return Pair{}, ErrLeaked
	}
	if !d.LiveAt(now) || !d.refresh.expires.After(now) {
		return Pair{}, ErrConnectionEnded
	}
	d.refresh.revoked = stamp.Of(now)
	return Pair{Access: AccessPrefix + RandomHex(24), Refresh: RefreshPrefix + RandomHex(24), Scopes: append([]string(nil), d.Scopes...)}, nil
}

// Consume records an admitted push. Expiry is checked at admission (ByToken),
// not again when a potentially long-running push finishes.
func (t *Ticket) Consume(now time.Time) error {
	if t.ConsumedAt.Valid {
		return ErrTicketConsumed
	}
	if t.RevokedAt.Valid {
		return ErrTicketRevoked
	}
	t.ConsumedAt = stamp.Of(now)
	return nil
}
