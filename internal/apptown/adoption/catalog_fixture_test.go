package adoption

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"
)

// recordCatalogFixture pins the current library for tests of versioned procedures.
func recordCatalogFixture(s *Service, ctx context.Context, project ids.ID, by string) error {
	_, err := s.RecordCurrent(ctx, project, AdoptionEdit, "record workflow catalog", by)
	return err
}
