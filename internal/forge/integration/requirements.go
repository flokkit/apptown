package integration

import (
	"context"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration/internal/integrationsql"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"slices"
	"strings"
)

// HasCapabilitiesIn keeps the required project attachments and their matching
// bindings stable until the caller's transaction ends. Engine-private resource
// rows and dangling bindings do not satisfy an adoption requirement.
func HasCapabilitiesIn(ctx context.Context, q database.Queryer, projectID ids.ID, required []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(required) == 0 {
		return nil
	}
	wanted := slices.Clone(required)
	slices.Sort(wanted)
	wanted = slices.Compact(wanted)
	for _, capability := range wanted {
		if strings.TrimSpace(capability) == "" {
			return fmt.Errorf("an adoption capability must be named")
		}
	}
	found, err := integrationsql.New(q.SQL()).AttachedCapabilitiesForShare(ctx, integrationsql.AttachedCapabilitiesForShareParams{ProjectID: projectID, Capabilities: wanted})
	if err != nil {
		return err
	}
	for _, capability := range wanted {
		if !slices.Contains(found, capability) {
			return fmt.Errorf("project %d is missing required capability %q", projectID, capability)
		}
	}
	return nil
}
