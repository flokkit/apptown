package adoption

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

// resolver resolves what a project is on right now, as an adoption
// receipt: every enabled import at its pinned version and position, every
// selected or system workflow at the version the prior revision pinned
// (a newly selected one at the library's latest), the policies, the
// extensions, and the packs.
type resolver struct {
	workflows Catalog
	db        *database.DB
}

func newResolver(db *database.DB, ways Catalog) *resolver {
	return &resolver{workflows: ways, db: db}
}

// Union records an explicit edit's view: existing selections retain their
// pins; newly selected workflows take the version selected at this edit.
func (rs *resolver) Union(ctx context.Context, projectID ids.ID, prior *RecordedRevision) (Receipt, error) {
	return rs.unionIn(ctx, rs.db, projectID, prior)
}
func (rs *resolver) unionIn(ctx context.Context, q database.Queryer, projectID ids.ID, prior *RecordedRevision) (Receipt, error) {
	r := EmptyReceipt()
	picks, err := currentPicksIn(ctx, q, projectID)
	if err != nil {
		return r, err
	}
	if len(picks) > 0 {
		r.Packs = pickNames(picks)
	}
	imports, err := importsIn(ctx, q, projectID)
	if err != nil {
		return r, err
	}
	for _, im := range imports {
		r.Guidance = append(r.Guidance, ReceiptBlock{Position: im.Position, BlockID: im.BlockID, Name: im.Name, Version: im.Version, Disabled: im.Disabled, Tier: im.Tier})
	}
	pinned := map[string]int{}
	if prior != nil {
		for _, w := range prior.Receipt.Workflows {
			pinned[w.Name] = w.Version
		}
	}
	if rs.workflows == nil {
		return r, ErrCatalogUnavailable
	}
	entries, err := rs.workflows.Workflows(ctx)
	if err != nil {
		return r, err
	}
	for _, e := range entries {
		if !e.Doc.Metadata.System && !laneSelectedIn(ctx, q, projectID, e.Name) {
			continue
		}
		version, yamlSrc := e.Version, e.YAML
		if v, ok := pinned[e.Name]; ok && v != e.Version {
			pinnedWorkflow, err := rs.workflows.Workflow(ctx, wow.WorkflowRef{Name: e.Name, Version: v})
			if err != nil {
				return r, fmt.Errorf("adopted workflow %s v%d is unavailable: %w", e.Name, v, err)
			}
			version, yamlSrc = v, pinnedWorkflow.YAML
		}
		origin := e.OriginRef()
		if at := strings.LastIndex(origin, "@"); at > 0 {
			origin = fmt.Sprintf("%s@%d", origin[:at], version) // the version the project runs, not the library's newest
		}
		r.Workflows = append(r.Workflows, ReceiptWorkflow{Name: e.Name, Version: version, Origin: origin, YAMLHash: HashText(yamlSrc), System: e.Doc.Metadata.System})
	}
	if r.Policies, err = policyReceiptLinesIn(ctx, q, projectID); err != nil {
		return r, err
	}
	exts, err := enabledExtensionsIn(ctx, q, projectID)
	if err != nil {
		return r, err
	}
	if exts != nil {
		r.Extensions = exts
	}
	return r, nil
}

func enabledExtensionsIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]string, error) {
	return adoptionsql.New(q.SQL()).EnabledExtensions(ctx, projectID)
}

func laneSelectedIn(ctx context.Context, q database.Queryer, projectID ids.ID, name string) bool {
	r, e := adoptionsql.New(q.SQL()).ProjectLane(ctx, adoptionsql.ProjectLaneParams{ProjectID: projectID, Name: name})
	return e == nil && r.Enabled
}
func (s *Service) UnionIn(ctx context.Context, q database.Queryer, projectID ids.ID, prior *RecordedRevision) (Receipt, error) {
	return s.resolver.unionIn(ctx, q, projectID, prior)
}
