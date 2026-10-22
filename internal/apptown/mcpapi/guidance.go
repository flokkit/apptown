package mcpapi

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
)

func guidanceOffer(ctx context.Context, adoptions *adoption.Service, projectID ids.ID) map[string]any {
	imports, err := adoptions.Imports(ctx, projectID)
	if err != nil {
		return nil
	}
	blocks := []map[string]any{}
	total := 0
	for _, im := range imports {
		if im.Disabled {
			continue
		}
		blocks = append(blocks, map[string]any{"name": im.Name, "version": im.Version, "bytes": im.Bytes})
		total += im.Bytes
	}
	out := map[string]any{"guidance": blocks, "guidanceBytes": total}
	if len(blocks) > 0 {
		out["guidanceNote"] = fmt.Sprintf("Strongly recommended before writing code here: get_project_guidance returns these %d blocks as one text (about %s). AppTown's own agents are steered by exactly this, and landing checks hold every Change to it.", len(blocks), humanBytes(total))
	}
	return out
}
func latestPack(ctx context.Context, db *database.DB, projectID ids.ID) (string, error) {
	applied, err := audit.Recent(ctx, db, audit.Filter{ProjectID: projectID, KindPrefix: "pack.applied", Limit: 1})
	if err != nil {
		return "", err
	}
	if len(applied) == 0 || applied[0].ProjectID != projectID {
		return "", nil
	}
	return applied[0].Subject, nil
}
