package wow

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

type Dependencies struct {
	Stock fs.FS // the release's shipped ways of working (stock/)
	// Shipped is the name of the ways-of-working update Stock came from:
	// the AppTown version that built it. Versions the stock sync publishes
	// record it (source and signer, origin.go).
	Shipped string
	// ToolLines says which tool lines the tool versions list names; a pack
	// may name only those (Pack.Tools). nil refuses every line.
	ToolLines toolLines
	// Importers says how many projects stay on earlier versions of a block
	// after a release moved it: adoption's fact, observed for the boot log.
	Importers importers
	// Checks is conformance's reading of a shipped block's checks: valid
	// and in canonical form, or refused. nil keeps them as written.
	Checks checksReader
	// Now is AppTown's clock, which stamps block versions; nil is the
	// real one.
	Now func() time.Time
}

// toolLines is the tool versions list, as a pack's tools are checked
// against it.
type toolLines interface {
	HasLine(tool, line string) bool
	// HasTool says whether the list names the tool at all: the tools a
	// check's appliesTo may name.
	HasTool(tool string) bool
}

type importers interface {
	Behind(ctx context.Context, blockID ids.ID, version int) (int, error)
}

type checksReader interface {
	Canonical(checksJSON string) (string, error)
}

// Service is the Ways of Working catalog, constructed once with private stores.
type Service struct {
	db        *database.DB
	deps      Dependencies
	library   *library
	workflows *workflows
	packs     *packs
	policies  *policies
	// shippedNames is the release's stock names, read once (withdrawn.go).
	shippedNames shippedOnce
}

func New(db *database.DB, deps Dependencies) *Service {
	s := &Service{db: db, deps: deps, library: NewLibrary(db), workflows: NewWorkflows(db), packs: NewPacks(db), policies: NewPolicies(db)}
	s.library.clock, s.workflows.clock, s.packs.clock, s.policies.clock = deps.Now, deps.Now, deps.Now, deps.Now
	s.packs.lines, s.packs.shipped = deps.ToolLines, deps.Shipped
	s.library.stock, s.workflows.stock = shippedOrigin(deps.Shipped), shippedOrigin(deps.Shipped)
	return s
}

// CatalogChange is the shipped catalog's entry of that name.
func (s *Service) CatalogChange(name string) (*CatalogChange, error) {
	all, err := s.CatalogChanges()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrCatalogMissing, name)
}

// CatalogChanges is the shipped catalog, in the file's order.
func (s *Service) CatalogChanges() ([]CatalogChange, error) {
	return readStockCatalog(s.deps.Stock)
}
func (s *Service) BlockByID(ctx context.Context, id ids.ID) (*Block, error) {
	return s.marked(s.library.byID(ctx, id))
}
func (s *Service) BlockByName(ctx context.Context, name string) (*Block, error) {
	return s.marked(s.library.ByName(ctx, name))
}

// marked says on a block read by name or id whether it is withdrawn.
func (s *Service) marked(b *Block, err error) (*Block, error) {
	if b != nil {
		b.Withdrawn = s.Withdrawn(KindGuidance, b.Origin, b.Name)
	}
	return b, err
}
func (s *Service) BlockVersionBody(ctx context.Context, blockID ids.ID, version int) (string, error) {
	return s.library.versionBody(ctx, blockID, version)
}

// Blocks is the guidance library: what is offered, so a withdrawn stock
// block is left out (it is still read by name or id).
func (s *Service) Blocks(ctx context.Context, f BlockFilter) ([]Block, error) {
	all, err := s.library.all(ctx, f)
	out := make([]Block, 0, len(all))
	for _, b := range all {
		if !s.Withdrawn(KindGuidance, b.Origin, b.Name) {
			out = append(out, b)
		}
	}
	return out, err
}
func (s *Service) BlockBytesByName(ctx context.Context, names []string) map[string]int {
	return s.library.bytesByName(ctx, names)
}
func (s *Service) CreateBlock(ctx context.Context, name, body, tags, who string) (*Block, error) {
	return s.library.Create(ctx, name, body, tags, who)
}
func (s *Service) BlockVersions(ctx context.Context, blockID ids.ID) ([]BlockVersion, error) {
	return s.library.versions(ctx, blockID)
}
func (s *Service) BumpBlock(ctx context.Context, id ids.ID, body, checksJSON, who, name string) error {
	return s.library.Bump(ctx, id, body, checksJSON, who, name)
}
func (s *Service) ResolvePins(ctx context.Context, pins []Pin) ([]ResolvedPin, error) {
	return s.library.Resolve(ctx, pins)
}

func (s *Service) BlockVersionChecks(ctx context.Context, blockID ids.ID, version int) (string, error) {
	return s.library.versionChecks(ctx, blockID, version)
}

// Pack is a pack with the contents of the publication it offers: what
// applying it installs (design/subsystems/ways-of-working-versions.md).
// PackDeclaration is what its author wrote.
func (s *Service) Pack(ctx context.Context, name string) (*Pack, error) {
	return s.packs.offeredContentsOf(ctx, name)
}

// PackDeclaration is a pack as its author wrote it: its own items and its
// inclusion, not resolved.
func (s *Service) PackDeclaration(ctx context.Context, name string) (*Pack, error) {
	r, err := s.packs.ByName(ctx, name)
	if err != nil {
		return nil, err
	}
	pk, err := r.pack()
	if err != nil {
		return nil, err
	}
	return &pk, nil
}
func (s *Service) Packs(ctx context.Context) ([]Pack, error) { return s.packs.offeredContents(ctx) }

// OfferedPacks is the packs a new project may be given, with their offered
// contents: on, and not the platform tier.
func (s *Service) OfferedPacks(ctx context.Context) ([]Pack, error) {
	all, err := s.packs.offeredContents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Pack, 0, len(all))
	for _, p := range all {
		if p.Enabled && !p.Platform {
			out = append(out, p)
		}
	}
	return out, nil
}
func (s *Service) OfferPack(ctx context.Context, name string, enabled bool, who string) error {
	return s.packs.setEnabled(ctx, name, enabled, who)
}
func (s *Service) DeletePack(ctx context.Context, name, who string) error {
	return s.packs.delete(ctx, name, who)
}

// RecommendPack moves a pack's recommended version, with its receipt.
func (s *Service) RecommendPack(ctx context.Context, name string, version int64, who string, whatIf ids.ID) error {
	return s.packs.recommend(ctx, name, version, who, whatIf)
}
func (s *Service) PublishPackVersion(ctx context.Context, name, description, spec, origin, versionName string) (int, error) {
	return s.packs.publishNamed(ctx, name, description, spec, origin, versionName)
}
func (s *Service) PackHistory(ctx context.Context, name string) (*PackHistory, error) {
	return s.packs.history(ctx, name)
}

func (s *Service) PackPublications(ctx context.Context, packID ids.ID) ([]publicationRow, error) {
	return s.packs.publications(ctx, packID)
}
func (s *Service) PublishPack(ctx context.Context, name, description, spec, origin string) (int, error) {
	return s.packs.Publish(ctx, name, description, spec, origin)
}
func (s *Service) CutPacksOnPinChange(ctx context.Context) ([]recut, error) {
	return s.packs.CutOnPinChange(ctx)
}
func (s *Service) LibraryPolicy(ctx context.Context, name string) *LibraryPolicy {
	return s.policies.LibraryByName(ctx, name)
}

// LibraryPolicies is the policy library: what is offered, so a withdrawn
// stock policy is left out. Projects that adopted one keep their copies.
func (s *Service) LibraryPolicies(ctx context.Context) ([]LibraryPolicy, error) {
	all, err := s.policies.libraryAll(ctx)
	out := make([]LibraryPolicy, 0, len(all))
	for _, lp := range all {
		if !s.Withdrawn(kindPolicy, lp.Origin, lp.Name) {
			out = append(out, lp)
		}
	}
	return out, err
}
func (s *Service) SaveLibraryPolicy(ctx context.Context, in LibraryPolicy, who string) error {
	return s.policies.SaveLibrary(ctx, in, who)
}
func (s *Service) DeleteLibraryPolicy(ctx context.Context, name, who string) error {
	return s.policies.DeleteLibrary(ctx, name, who)
}
func (s *Service) WorkflowVersion(ctx context.Context, name string, version int) (int, string, error) {
	return s.workflows.version(ctx, name, version)
}
func (s *Service) WorkflowOrigin(ctx context.Context, name string) (string, error) {
	return s.workflows.origin(ctx, name)
}

// Workflows is every library workflow at its latest version, withdrawn
// ones included and marked: adoption resolves a project's selections from
// it, so it is not filtered. What offers a workflow leaves out the
// withdrawn ones that are not already selected.
func (s *Service) Workflows(ctx context.Context) ([]WorkflowEntry, error) {
	all, err := s.workflows.latest(ctx)
	for i := range all {
		all[i].Withdrawn = s.Withdrawn(KindWorkflow, all[i].Origin, all[i].Name)
	}
	return all, err
}
func (s *Service) CustomWorkflowNames(ctx context.Context) ([]string, error) {
	return s.workflows.customNames(ctx)
}
func (s *Service) PublishWorkflow(ctx context.Context, yamlSrc, who string) (name string, version int, err error) {
	return s.workflows.Put(ctx, yamlSrc, who)
}
func (s *Service) DeleteWorkflow(ctx context.Context, name, who string) error {
	return s.workflows.delete(ctx, name, who)
}
func (s *Service) SystemWorkflow(ctx context.Context, stockName string) string {
	return s.workflows.SystemFor(ctx, stockName)
}
func (s *Service) IsSystemWorkflow(ctx context.Context, name string) bool {
	return s.workflows.isSystem(ctx, name)
}
func (s *Service) BindSystemWorkflow(ctx context.Context, stock, name, who string) error {
	return s.workflows.BindSystem(ctx, stock, name, who)
}
func (s *Service) ResetSystemWorkflow(ctx context.Context, stock, who string) error {
	return s.workflows.ResetSystem(ctx, stock, who)
}
