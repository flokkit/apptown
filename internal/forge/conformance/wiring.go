package conformance

import (
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
)

// New builds conformance; now is the forge's clock, which stamps reviews,
// verdicts, exceptions and fixes and judges an exception's expiry (nil is
// the real clock).
func New(db *database.DB, authority access.Authorizer, source Source, now func() time.Time) *Service {
	return &Service{db: db, records: reviewStore{db: db, checks: source, clock: now}, authority: commandAuthority{db: db, checked: authority}, source: source}
}
