package access

import (
	"errors"
	"time"

	"flokkit.io/forge/internal/stamp"
)

type Event struct{ Kind, Actor, Subject string }

var ErrGrantRevoked = errors.New("grant already revoked")

func (g Grant) Live(at time.Time) bool {
	return !g.RevokedAt.Valid && (!g.ExpiresAt.Valid || g.ExpiresAt.Time.After(at))
}

// Applies covers capability and scope.
func (g Grant) Applies(c Capability, r Resource) bool {
	return GrantHolds(g, c) && GrantApplies(g, c, r)
}

func (g *Grant) Revoke(by Principal, now time.Time) (Event, error) {
	if g.RevokedAt.Valid {
		return Event{}, ErrGrantRevoked
	}
	g.RevokedAt = stamp.Of(now)
	return Event{Kind: "grant.revoked", Actor: by.Name, Subject: g.ID.String()}, nil
}
