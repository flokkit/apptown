package adoption

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
)

// Import is one line of a project's guidance manifest.
type Import struct {
	Position      int    `json:"position"`
	BlockID       ids.ID `json:"blockId"`
	Name          string `json:"name"`
	Version       int    `json:"version"`
	LatestVersion int    `json:"latestVersion"`
	Disabled      bool   `json:"disabled"`
	Bytes         int    `json:"bytes"` // of the pinned version
	Tier          string `json:"tier,omitempty"`
	Conflicts     string `json:"conflicts,omitempty"`
	// Origin is the block's ("stock" or "" for yours); Withdrawn says it is
	// stock the running release no longer ships: kept, still rendered and
	// checked, and shown as no longer shipped.
	Origin    string `json:"origin"`
	Withdrawn bool   `json:"withdrawn,omitempty"`
}

// BlockUse is a project importing a block, at any version.
type BlockUse struct {
	ProjectSlug string `json:"projectSlug"`
	Version     int    `json:"version"`
	Disabled    bool   `json:"disabled"`
}

// UsedBy is which projects import a block, at any version.
func (s *libraryStore) UsedBy(ctx context.Context, blockID ids.ID) ([]BlockUse, error) {
	rows, err := adoptionsql.New(s.db.SQL()).BlockUses(ctx, blockID)
	out := make([]BlockUse, 0, len(rows))
	for _, r := range rows {
		out = append(out, BlockUse{ProjectSlug: r.Slug, Version: int(r.Version), Disabled: r.Disabled})
	}
	return out, err
}

// Imports is a project's manifest in position order.
func (s *libraryStore) Imports(ctx context.Context, projectID ids.ID) ([]Import, error) {
	return importsIn(ctx, s.db, projectID)
}
func importsIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]Import, error) {
	rows, err := adoptionsql.New(q.SQL()).GuidanceImportsMeta(ctx, projectID)
	var out []Import
	for _, r := range rows {
		im, b := r.GuidanceImport, r.GuidanceBlock
		out = append(out, Import{Position: int(im.Position), BlockID: im.BlockID, Name: b.Name, Version: int(im.Version), LatestVersion: int(b.LatestVersion), Disabled: im.Disabled, Bytes: int(r.BodyBytes), Tier: b.Tier, Conflicts: b.Conflicts, Origin: b.Origin})
	}
	return out, err
}

// PutImports replaces a project's manifest in one transaction, so a
// manifest is never half-written.
func (s *libraryStore) PutImports(ctx context.Context, projectID ids.ID, pins []wow.ResolvedPin) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error { return PutImportsIn(ctx, tx, projectID, pins, clock.Or(s.clock)()) })
}

// PutImportsIn replaces a project's manifest inside the caller's
// transaction, in the order given.
func PutImportsIn(ctx context.Context, tx *database.Tx, projectID ids.ID, pins []wow.ResolvedPin, at time.Time) error {
	q := adoptionsql.New(tx.SQL())
	if _, err := q.DeleteGuidanceImportsForProject(ctx, projectID); err != nil {
		return err
	}
	for i, p := range pins {
		if _, err := q.InsertGuidanceImport(ctx, adoptionsql.InsertGuidanceImportParams{ID: ids.New(at), ProjectID: projectID, Position: int64(i), BlockID: p.BlockID, Version: int64(p.Version), Disabled: p.Disabled}); err != nil {
			return err
		}
	}
	return nil
}

// Compile deterministically materializes a project's effective guidance:
// enabled imports, in manifest order, pinned bodies, joined with
// separators. The hash is the identity of what actually steered a run.
func (s *libraryStore) Compile(ctx context.Context, projectID ids.ID) (Resolved, error) {
	imports, err := s.Imports(ctx, projectID)
	if err != nil {
		return Resolved{}, err
	}
	compiled, err := compileGuidance(ctx, s.ways, imports, "import ")
	if err != nil {
		return Resolved{}, err
	}
	return compiled, nil
}

// PlatformBlocksMissingFrom is the platform-tier blocks a project does
// not import, by name: what creation gives every repo-backed project.
func (s *libraryStore) PlatformBlocksMissingFrom(ctx context.Context, projectID ids.ID) ([]wow.Block, error) {
	rows, err := adoptionsql.New(s.db.SQL()).MissingPlatformBlocks(ctx, adoptionsql.MissingPlatformBlocksParams{ProjectID: projectID, Tier: wow.TierPlatform.String()})
	out := make([]wow.Block, 0, len(rows))
	for _, r := range rows {
		out = append(out, wow.Block{ID: r.ID, Name: r.Name, Tags: r.Tags, LatestVersion: int(r.LatestVersion), Origin: r.Origin, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano), Tier: r.Tier, Conflicts: r.Conflicts})
	}
	return out, err
}

// AppendPlatformImport adds a platform block after a project's own
// imports at the block's latest version, with its receipt, in one
// transaction.
func (s *libraryStore) AppendPlatformImport(ctx context.Context, projectID ids.ID, b wow.Block) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := adoptionsql.New(tx.SQL())
		pos, err := q.NextImportPosition(ctx, projectID)
		if err != nil {
			return err
		}
		if _, err := q.InsertGuidanceImport(ctx, adoptionsql.InsertGuidanceImportParams{ID: ids.New(clock.Or(s.clock)()), ProjectID: projectID, Position: pos, BlockID: b.ID, Version: int64(b.LatestVersion)}); err != nil {
			return err
		}
		return audit.At(clock.Or(s.clock)()).RecordIn(ctx, tx, "apptown-system", projectID, "guidance.platform.adopted", b.Name, map[string]any{"version": b.LatestVersion})
	})
}

type libraryStore struct {
	db    *database.DB
	ways  Catalog
	clock func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func newLibrary(db *database.DB, ways Catalog) *libraryStore {
	return &libraryStore{db: db, ways: ways}
}
