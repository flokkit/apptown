package adoption

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	"fmt"
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
