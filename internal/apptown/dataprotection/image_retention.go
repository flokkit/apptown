package dataprotection

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/ids"
)

// KeptBackupReleases names the releases kept copies were taken with, including
// partial copies. A failed read is an error, never an empty keep set.
func (s *Service) KeptBackupReleases(ctx context.Context) ([]ids.ID, error) {
	return protectionsql.New(s.db.SQL()).KeptBackupReleases(ctx)
}
