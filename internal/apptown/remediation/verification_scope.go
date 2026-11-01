package remediation

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// VerificationScope pins an observation, not a credential lifetime. Fresh reads
// prevent a completed or superseded attempt from admitting another review.
type VerificationScope struct {
	db   *database.DB
	item Item
}

func (s *Service) VerificationScope(item Item) VerificationScope {
	return VerificationScope{db: s.db, item: item}
}
func (s VerificationScope) ProjectID() ids.ID { return s.item.ProjectID }
func (s VerificationScope) Current(ctx context.Context) (bool, error) {
	if s.db == nil {
		return false, access.ErrAuthorityUnavailable
	}
	current, err := readItem(ctx, s.db, s.item.ID)
	return err == nil && current.ProjectID == s.item.ProjectID && current.State == itemVerifying && sameObservation(current, s.item), err
}
func (s VerificationScope) RequireRead(ctx context.Context) error {
	if s.db == nil {
		return access.ErrAuthorityUnavailable
	}
	current, err := queries(s.db).ItemByID(ctx, s.item.ID)
	if err != nil {
		return err
	}
	if current.ProjectID != s.item.ProjectID {
		return &access.Denied{Code: access.DenyOutsideScope, Reason: "the remediation belongs to another project"}
	}
	return nil
}
