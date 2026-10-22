package adoption

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

// MemoryDefault reads the primary pack's exact adopted publication in one
// snapshot. Absence is a fallback; a broken pin or stored declaration is not.
func (s *Service) MemoryDefault(ctx context.Context, projectID ids.ID) (appcontract.MemoryDefault, string, int64, error) {
	r, err := adoptionsql.New(s.db.SQL()).PrimaryPackMemoryDefault(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return appcontract.MemoryDefault{}, "", 0, nil
	}
	if err != nil {
		return appcontract.MemoryDefault{}, "", 0, err
	}
	if r.Pack == "" {
		return appcontract.MemoryDefault{}, "", 0, nil
	}
	if r.Version <= 0 || r.Spec == "" {
		return appcontract.MemoryDefault{}, "", 0, fmt.Errorf("adopted pack %s has no readable publication", r.Pack)
	}
	var spec wow.PackSpec
	if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
		return appcontract.MemoryDefault{}, "", 0, fmt.Errorf("adopted pack %s v%d: %w", r.Pack, r.Version, err)
	}
	return spec.Resources, r.Pack, r.Version, nil
}
