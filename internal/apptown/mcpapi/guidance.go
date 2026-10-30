package mcpapi

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
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
		out["guidanceNote"] = fmt.Sprintf("Strongly recommended before writing code here: get_project_guidance returns these %d blocks as one text (about %s), with the how-to skills the text names beside it. AppTown's own agents are steered by exactly this, and landing checks hold every Change to it.", len(blocks), humanBytes(total))
	}
	return out
}

// projectPacks names the packs a project is on: its memberships, every
// one of them, the platform pack included.
func projectPacks(ctx context.Context, adoptions *adoption.Service, projectID ids.ID) ([]string, error) {
	records, err := adoptions.ProjectPacks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range records {
		out = append(out, r.Pack)
	}
	return out, nil
}
