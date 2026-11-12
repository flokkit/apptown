package adoption

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/fix"
)

// Fixes reads only enabled guidance at the supplied receipt's immutable versions.
func (s *Service) Fixes(ctx context.Context, source Source) ([]fix.Declaration, error) {
	revision, err := source.receipt("fixes")
	if err != nil {
		return nil, err
	}
	var out []fix.Declaration
	for _, b := range revision.Receipt.Guidance {
		if b.Disabled {
			continue
		}
		if b.Version <= 0 {
			return nil, fmt.Errorf("%s has no pinned version", b.Name)
		}
		if s.deps.Ways == nil {
			return nil, errCatalogUnavailable
		}
		v, err := s.deps.Ways.Block(ctx, wow.BlockRef{ID: b.BlockID, Version: b.Version})
		if err != nil {
			return nil, err
		}
		ds, err := fix.Parse(v.FixesJSON)
		if err != nil {
			return nil, fmt.Errorf("%s v%d: %w", b.Name, b.Version, err)
		}
		out = append(out, ds...)
	}
	return out, nil
}
