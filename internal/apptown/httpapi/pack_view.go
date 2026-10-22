package httpapi

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"

	"github.com/flokkit/apptown/internal/database"
)

// Read membership and the selected version's contents together. Missing version
// rows retain their membership but have no known contents, as in existing views.
func readProjectPacks(ctx context.Context, db database.Queryer, projectID ids.ID) ([]projectPack, error) {
	records, err := adoption.ProjectPacksIn(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]projectPack, 0, len(records))
	for _, r := range records {
		pp := projectPack{Pack: r.Pack, Version: r.Version, Recommended: r.Recommended, Reason: r.Reason, AdoptedBy: r.AdoptedBy, AdoptedAt: r.AdoptedAt}
		if err := json.Unmarshal([]byte(r.SpecJSON), &pp.spec); err != nil {
			return nil, err
		}
		pp.Platform = pp.spec.Platform
		out = append(out, pp)
	}
	return out, nil
}
