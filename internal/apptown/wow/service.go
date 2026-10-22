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
	// Importers says how many projects stay on earlier versions of a block
	// after a release moved it: adoption's fact, observed for the boot log.
	Importers Importers
	// Checks is conformance's reading of a shipped block's checks: valid
	// and in canonical form, or refused. nil keeps them as written.
	Checks ChecksReader
	// Now is AppTown's clock, which stamps block versions; nil is the
	// real one.
	Now func() time.Time
}

type Importers interface {
	Behind(ctx context.Context, blockID ids.ID, version int) (int, error)
}

type ChecksReader interface {
	Canonical(checksJSON string) (string, error)
}

// Service is the Ways of Working catalog, constructed once with private stores.
type Service struct {
	db        *database.DB
	deps      Dependencies
	library   *Library
	workflows *Workflows
	packs     *Packs
	policies  *Policies
	// shippedNames is the release's stock names, read once (withdrawn.go).
	shippedNames shippedOnce
}

func New(db *database.DB, deps Dependencies) *Service {
	s := &Service{db: db, deps: deps, library: NewLibrary(db), workflows: NewWorkflows(db), packs: NewPacks(db), policies: NewPolicies(db)}
	s.library.clock, s.workflows.clock, s.packs.clock, s.policies.clock = deps.Now, deps.Now, deps.Now, deps.Now
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
	return ReadStockCatalog(s.deps.Stock)
}
func (s *Service) BlockByID(ctx context.Context, id ids.ID) (*Block, error) {
	return s.marked(s.library.ByID(ctx, id))
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
	return s.library.VersionBody(ctx, blockID, version)
}

// Blocks is the guidance library: what is offered, so a withdrawn stock
// block is left out (it is still read by name or id).
func (s *Service) Blocks(ctx context.Context, f BlockFilter) ([]Block, error) {
	all, err := s.library.All(ctx, f)
	out := make([]Block, 0, len(all))
	for _, b := range all {
		if !s.Withdrawn(KindGuidance, b.Origin, b.Name) {
			out = append(out, b)
		}
	}
	return out, err
}
func (s *Service) BlockBytesByName(ctx context.Context, names []string) map[string]int {
	return s.library.BytesByName(ctx, names)
}
func (s *Service) CreateBlock(ctx context.Context, name, body, tags, who string) (*Block, error) {
	return s.library.Create(ctx, name, body, tags, who)
}
func (s *Service) BlockVersions(ctx context.Context, blockID ids.ID) ([]BlockVersion, error) {
	return s.library.Versions(ctx, blockID)
}
func (s *Service) BumpBlock(ctx context.Context, id ids.ID, body, checksJSON, who, name string) error {
	return s.library.Bump(ctx, id, body, checksJSON, who, name)
}
func (s *Service) ResolvePins(ctx context.Context, pins []Pin) ([]ResolvedPin, error) {
	return s.library.Resolve(ctx, pins)
}
func (s *Service) BlockNames(ctx context.Context, prefixes ...string) ([]string, error) {
	return s.library.Names(ctx, prefixes...)
}
func (s *Service) BlockVersionChecks(ctx context.Context, blockID ids.ID, version int) (string, error) {
	return s.library.VersionChecks(ctx, blockID, version)
}

// Pack is a pack with the contents of the publication it offers: what
// applying it installs (design/subsystems/ways-of-working-versions.md).
// PackDeclaration is what its author wrote.
func (s *Service) Pack(ctx context.Context, name string) (*Pack, error) {
	return s.packs.OfferedContentsOf(ctx, name)
}

// PackDeclaration is a pack as its author wrote it: its own items and its
// inclusion, not resolved.
func (s *Service) PackDeclaration(ctx context.Context, name string) (*Pack, error) {
	r, err := s.packs.ByName(ctx, name)
	if err != nil {
		return nil, err
	}
	pk, err := r.Pack()
	if err != nil {
		return nil, err
	}
	return &pk, nil
}
func (s *Service) Packs(ctx context.Context) ([]Pack, error) { return s.packs.OfferedContents(ctx) }

// OfferedPacks is the packs a new project may be given, with their offered
// contents: on, and not the platform tier.
func (s *Service) OfferedPacks(ctx context.Context) ([]Pack, error) {
	all, err := s.packs.OfferedContents(ctx)
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
	return s.packs.SetEnabled(ctx, name, enabled, who)
}
func (s *Service) DeletePack(ctx context.Context, name, who string) error {
	return s.packs.Delete(ctx, name, who)
}

// RecommendPack moves a pack's recommended version, with its receipt, and
// cuts follow versions of the packs that include it.
func (s *Service) RecommendPack(ctx context.Context, name string, version int64, who string, whatIf ids.ID) error {
	return s.packs.Recommend(ctx, name, version, who, whatIf)
}
func (s *Service) PublishPackVersion(ctx context.Context, name, description, spec, origin, versionName string) (int, error) {
	return s.packs.PublishNamed(ctx, name, description, spec, origin, versionName)
}
func (s *Service) PackHistory(ctx context.Context, name string) (*PackHistory, error) {
	return s.packs.History(ctx, name)
}
func (s *Service) PackVersions(ctx context.Context, name string) ([]Publication, error) {
	return s.packs.PublicationsOf(ctx, name)
}
func (s *Service) PackPublications(ctx context.Context, packID ids.ID) ([]PublicationRow, error) {
	return s.packs.Publications(ctx, packID)
}
func (s *Service) PublishPack(ctx context.Context, name, description, spec, origin string) (int, error) {
	return s.packs.Publish(ctx, name, description, spec, origin)
}
func (s *Service) CutPacksOnPinChange(ctx context.Context) ([]Recut, error) {
	return s.packs.CutOnPinChange(ctx)
}
func (s *Service) LibraryPolicy(ctx context.Context, name string) *LibraryPolicy {
	return s.policies.LibraryByName(ctx, name)
}

// LibraryPolicies is the policy library: what is offered, so a withdrawn
// stock policy is left out. Projects that adopted one keep their copies.
func (s *Service) LibraryPolicies(ctx context.Context) ([]LibraryPolicy, error) {
	all, err := s.policies.LibraryAll(ctx)
	out := make([]LibraryPolicy, 0, len(all))
	for _, lp := range all {
		if !s.Withdrawn(KindPolicy, lp.Origin, lp.Name) {
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
	return s.workflows.Version(ctx, name, version)
}
func (s *Service) WorkflowOrigin(ctx context.Context, name string) (string, error) {
	return s.workflows.Origin(ctx, name)
}

// Workflows is every library workflow at its latest version, withdrawn
// ones included and marked: adoption resolves a project's selections from
// it, so it is not filtered. What offers a workflow leaves out the
// withdrawn ones that are not already selected.
func (s *Service) Workflows(ctx context.Context) ([]WorkflowEntry, error) {
	all, err := s.workflows.Latest(ctx)
	for i := range all {
		all[i].Withdrawn = s.Withdrawn(KindWorkflow, all[i].Origin, all[i].Name)
	}
	return all, err
}
func (s *Service) CustomWorkflowNames(ctx context.Context) ([]string, error) {
	return s.workflows.CustomNames(ctx)
}
func (s *Service) PublishWorkflow(ctx context.Context, yamlSrc, who string) (name string, version int, err error) {
	return s.workflows.Put(ctx, yamlSrc, who)
}
func (s *Service) DeleteWorkflow(ctx context.Context, name, who string) error {
	return s.workflows.Delete(ctx, name, who)
}
func (s *Service) SystemWorkflow(ctx context.Context, stockName string) string {
	return s.workflows.SystemFor(ctx, stockName)
}
func (s *Service) IsSystemWorkflow(ctx context.Context, name string) bool {
	return s.workflows.IsSystem(ctx, name)
}
func (s *Service) BindSystemWorkflow(ctx context.Context, stock, name, who string) error {
	return s.workflows.BindSystem(ctx, stock, name, who)
}
func (s *Service) ResetSystemWorkflow(ctx context.Context, stock, who string) error {
	return s.workflows.ResetSystem(ctx, stock, who)
}
