package adoption

import (
	"context"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// errCatalogUnavailable means an operation needs the catalog, but none was supplied.
var errCatalogUnavailable = errors.New("ways of working catalog is unavailable")

// Operation preserves the separate authorities for selection and pack installation.
type Operation uint8

const (
	OperationSelect Operation = iota + 1
	OperationApplyPack
)

type authorization struct {
	Operation Operation
	ProjectID ids.ID
	Actor     string
	Weakens   bool
}

// catalog is the exact catalog observation used when resolving a project's existing selections.
type catalog interface {
	Block(context.Context, wow.BlockRef) (wow.BlockObservation, error)
	Workflow(context.Context, wow.WorkflowRef) (wow.WorkflowObservation, error)
	Workflows(ctx context.Context) ([]wow.WorkflowEntry, error)
	PackPublication(context.Context, string, int64) (wow.PackVersion, error)
	// Withdrawn says whether a stored stock entry is one the running release
	// no longer ships: not offered, kept and labelled where it is used.
	Withdrawn(k wow.Kind, origin, name string) bool
}

// capabilityLocker is one project-scoped transactional observation.
type capabilityLocker interface {
	HasCapabilitiesIn(context.Context, database.Queryer, ids.ID, []string) error
}

// TargetRecorder participates in Work's existing admission transaction.
type TargetRecorder interface {
	RecordTargetIn(context.Context, *database.Tx, Target) (ScopedTarget, error)
}
type UpliftRequest struct {
	Target    TargetRecorder
	Candidate Candidate
	Actor     string
	// Basis is the line the Change's brief gives for why it was opened (the
	// what-if it was decided on); empty when none.
	Basis string
	// InAdvance is the sentences the opener was shown and said yes to in
	// advance (workflow's InAdvanceQuestion); empty when they gave none.
	InAdvance []string
}

// Work retains the one transaction that opens a Change and records its target.
type upliftOpener interface {
	OpenUplift(context.Context, UpliftRequest) (UpliftChange, error)
}

// UpliftChange is the change an uplift opened: its id, and its number
// within the project, which is how people name it; and what came of
// starting its working run. Run is set when a run was admitted; RunError
// says why an admitted run did not get going (its workspace or dispatch
// failed), and NotStarted why none was admitted (no working lane, or the
// start was refused). The Change stands in every case.
type UpliftChange struct {
	ID         ids.ID
	Number     int64
	Run        ids.ID
	RunNumber  int64
	Workflow   string
	RunError   string
	NotStarted string
}
type Dependencies struct {
	RoutinePublications routinePublications
	Authority           access.ReadAuthorizer
	Ways                catalog
	Capabilities        capabilityLocker
	Uplift              upliftOpener
	// Copies says whether a project is an eval's throwaway copy (Project's
	// purpose), read through the caller's transaction: only a copy takes
	// another project's recorded way of working (copy.go). Nil refuses
	// every copy.
	Copies copyPurpose
	// Reviews admits a conformance review of a project whose adoption a
	// transaction changed, in that transaction. The composition root wires
	// it; nil requests none (owner tests that are not about it).
	Reviews reviewRequester
	// Now is AppTown's clock, which stamps each revision; nil is the
	// real one.
	Now func() time.Time
}

// Service owns what each project has adopted, with its history and installed selections.
type Service struct {
	db        *database.DB
	deps      Dependencies
	adoptions *adoptionStore
	library   *libraryStore
	policies  *policyStore
	lanes     *laneStore
	resolver  *resolver
}

func New(db *database.DB, deps Dependencies) *Service {
	adoptions := newAdoptions(db)
	adoptions.clock = deps.Now
	adoptions.reviews = deps.Reviews
	policies := newPolicies(db)
	policies.clock = deps.Now
	library := newLibrary(db, deps.Ways)
	library.clock = deps.Now
	return &Service{db: db, deps: deps, adoptions: adoptions, library: library, policies: policies, lanes: newLanes(db), resolver: newResolver(db, deps.Ways)}
}
func (s *Service) CurrentReceipt(ctx context.Context, projectID ids.ID) (*RecordedRevision, error) {
	return s.adoptions.current(ctx, projectID)
}
func (s *Service) ByID(ctx context.Context, id ids.ID) (*RecordedRevision, error) {
	return s.adoptions.byID(ctx, id)
}
func (s *Service) History(ctx context.Context, projectID ids.ID, limit int) ([]RecordedRevision, error) {
	return s.adoptions.history(ctx, projectID, limit)
}
func (s *Service) Advance(ctx context.Context, projectID ids.ID, kind AdoptionKind, reason, by string, resolve func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error)) (*RecordedRevision, error) {
	return s.adoptions.advance(ctx, projectID, kind, reason, by, false, resolve)
}

// Record is Advance for a change the receipt does not show, such as a pack
// membership move: it records a revision even when the receipt is unchanged.
func (s *Service) Record(ctx context.Context, projectID ids.ID, kind AdoptionKind, reason, by string, resolve func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error)) (*RecordedRevision, error) {
	return s.adoptions.advance(ctx, projectID, kind, reason, by, true, resolve)
}
func (s *Service) BlockUses(ctx context.Context, blockID ids.ID) ([]BlockUse, error) {
	return s.library.usedBy(ctx, blockID)
}
func (s *Service) Imports(ctx context.Context, projectID ids.ID) ([]Import, error) {
	return s.library.imports(ctx, projectID)
}
func (s *Service) PutImports(ctx context.Context, projectID ids.ID, pins []wow.ResolvedPin) error {
	return s.library.putImports(ctx, projectID, pins)
}
func (s *Service) Compile(ctx context.Context, projectID ids.ID) (Resolved, error) {
	return s.library.compile(ctx, projectID)
}
func (s *Service) PlatformBlocksMissingFrom(ctx context.Context, projectID ids.ID) ([]wow.Block, error) {
	all, err := s.library.platformBlocksMissingFrom(ctx, projectID)
	if err != nil || s.deps.Ways == nil {
		return all, err
	}
	out := make([]wow.Block, 0, len(all))
	for _, b := range all {
		if !s.deps.Ways.Withdrawn(wow.KindGuidance, b.Origin, b.Name) {
			out = append(out, b) // a withdrawn block is not given to a new project
		}
	}
	return out, nil
}
func (s *Service) AppendPlatformImport(ctx context.Context, projectID ids.ID, b wow.Block) error {
	return s.library.appendPlatformImport(ctx, projectID, b)
}

func (s *Service) InstallPolicy(ctx context.Context, projectID ids.ID, name, description, rule string, with map[string]any, origin, who string) error {
	return s.policies.install(ctx, projectID, name, description, rule, with, origin, who)
}
func (s *Service) RemovePolicy(ctx context.Context, id ids.ID, who string) error {
	return s.policies.remove(ctx, id, who)
}

// PolicyAdoptions is every (policy name, project) pair in the projects the
// verified caller can read; other projects' adoptions are left out, so usage
// counts never reveal them.
func (s *Service) PolicyAdoptions(ctx context.Context) ([]policyAdoption, error) {
	if s.deps.Authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	idList, err := s.deps.Authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return nil, err
	}
	visible := make(map[ids.ID]bool, len(idList))
	for _, id := range idList {
		visible[id] = true
	}
	all, err := s.policies.adoptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]policyAdoption, 0, len(all))
	for _, a := range all {
		if visible[a.ProjectID] {
			out = append(out, a)
		}
	}
	return out, nil
}
func (s *Service) Union(ctx context.Context, projectID ids.ID, prior *RecordedRevision) (Receipt, error) {
	return s.resolver.union(ctx, projectID, prior)
}
func (s *Service) RefreshPolicyWording(ctx context.Context) error {
	return s.policies.refreshAdoptedWording(ctx)
}

// LaneSelected says whether a project selected a lane. A failed read is an
// error, never "not selected".
func (s *Service) LaneSelected(ctx context.Context, projectID ids.ID, name string) (bool, error) {
	return s.lanes.selected(ctx, projectID, name)
}

// SelectLane selects or deselects a lane and records the adoption revision
// it makes, attributed to by, in one transaction: a selection never stands
// without its history, nor history without its selection, and the revision
// names who made it. A selection that changes nothing records nothing.
func (s *Service) SelectLane(ctx context.Context, projectID ids.ID, name string, enabled bool, by string) error {
	if by == "" {
		return fault.New(fault.Invalid, "a lane selection must name who made it")
	}
	reason := "lane " + name + map[bool]string{true: " selected", false: " deselected"}[enabled]
	_, err := s.Advance(ctx, projectID, AdoptionEdit, reason, by, func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		if err := setLaneIn(ctx, tx, projectID, name, enabled, now); err != nil {
			return Receipt{}, err
		}
		return s.UnionIn(ctx, tx, projectID, prior)
	})
	return err
}

func (s *Service) EnabledExtensions(ctx context.Context, projectID ids.ID) ([]string, error) {
	return enabledExtensionsIn(ctx, s.db, projectID)
}
func (s *Service) ProjectPolicies(ctx context.Context, q database.Queryer, projectID ids.ID) ([]Policy, error) {
	return ReadProjectPolicies(ctx, q, projectID)
}
func (s *Service) CheckWorkflow(ctx context.Context, q database.Queryer, projectID ids.ID, name string, spec *schema.Spec) ([]Violation, error) {
	return checkWorkflow(ctx, q, projectID, name, spec, integrationCapabilities{})
}
func (s *Service) ProjectConformance(ctx context.Context, q database.Queryer, projectID ids.ID) ([]Violation, error) {
	return projectConformance(ctx, q, projectID, integrationCapabilities{})
}
func ImportersBehindIn(ctx context.Context, q database.Queryer, blockID ids.ID, version int) (int, error) {
	n, e := adoptionsql.New(q.SQL()).ImportersBehind(ctx, adoptionsql.ImportersBehindParams{BlockID: blockID, Version: int64(version)})
	return int(n), e
}

func WorkflowSelectionsIn(ctx context.Context, q database.Queryer) (map[string][]string, error) {
	rows, e := adoptionsql.New(q.SQL()).WorkflowSelections(ctx)
	if e != nil {
		return nil, e
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.Name] = append(out[r.Name], r.Slug)
	}
	return out, nil
}

// AdoptIn is the checked service door. The source is observed under the same
// transaction as the object and its receipt; callers cannot erase weakening or
// published capability requirements by editing a candidate's summary fields.
func (s *Service) AdoptIn(ctx context.Context, tx *database.Tx, projectID ids.ID, c Candidate, expected int64, who, reason string, operation Operation) (*Adoption, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.deps.Authority == nil {
		return nil, errors.New("adoption authority is unavailable")
	}
	if operation == OperationApplyPack {
		if err := validatePackApplication(c); err != nil {
			return nil, err
		}
	} else if err := requireSelectionCandidate(c); err != nil {
		return nil, err
	}
	if c.ProjectID != projectID {
		return nil, errors.New("candidate belongs to another project")
	}
	current, err := CurrentForUpdateIn(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.validateCandidateIn(ctx, tx, projectID, c); err != nil {
		return nil, err
	}
	weakens, err := s.weakeningsIn(ctx, tx, projectID, c)
	if err != nil {
		return nil, err
	}
	if err := s.require(ctx, authorization{Operation: operation, ProjectID: projectID, Actor: who, Weakens: len(weakens) > 0}); err != nil {
		return nil, err
	}
	return adoptIn(ctx, tx, current, c, expected, who, reason, func(required []string) error { return s.requireCapabilities(ctx, tx, projectID, required) }, s.deps.Reviews, s.adoptions.now())
}
func (s *Service) requireCapabilities(ctx context.Context, q database.Queryer, projectID ids.ID, required []string) error {
	if len(required) == 0 {
		return nil
	}
	if s.deps.Capabilities == nil {
		return errCapabilityUnavailable
	}
	return s.deps.Capabilities.HasCapabilitiesIn(ctx, q, projectID, required)
}

// OpenUplift opens the Change that carries an adoption. inAdvance, when
// given, is the opener's yes in advance: the sentences they were shown.
func (s *Service) OpenUplift(ctx context.Context, projectID ids.ID, c Candidate, who, basis string, inAdvance []string) (UpliftChange, error) {
	if err := requireSelectionCandidate(c); err != nil {
		return UpliftChange{}, err
	}
	if s.deps.Authority == nil {
		return UpliftChange{}, errors.New("adoption authority is unavailable")
	}
	if c.ProjectID != projectID {
		return UpliftChange{}, errors.New("candidate belongs to another project")
	}
	if err := s.require(ctx, authorization{Operation: OperationSelect, ProjectID: projectID, Actor: who}); err != nil {
		return UpliftChange{}, err
	}
	if s.deps.Uplift == nil {
		return UpliftChange{}, errors.New("adoption uplift is unavailable")
	}
	return s.deps.Uplift.OpenUplift(ctx, UpliftRequest{Candidate: c, Actor: who, Target: s, Basis: basis, InAdvance: inAdvance})
}

// RecordTargetIn is called after Work acquires its project-first admission lock.
// Target history is immutable evidence; it never advances the current aggregate.
func (s *Service) RecordTargetIn(ctx context.Context, tx *database.Tx, in Target) (ScopedTarget, error) {
	if err := requireSelectionCandidate(in.Candidate); err != nil {
		return ScopedTarget{}, err
	}
	if s.deps.Authority == nil {
		return ScopedTarget{}, errors.New("adoption authority is unavailable")
	}
	if _, err := CurrentForUpdateIn(ctx, tx, in.ProjectID); err != nil {
		return ScopedTarget{}, err
	}
	if err := s.validateCandidateIn(ctx, tx, in.ProjectID, in.Candidate); err != nil {
		return ScopedTarget{}, err
	}
	if err := s.validateTargetChecksIn(ctx, tx, in.ProjectID, in.Candidate); err != nil {
		return ScopedTarget{}, err
	}
	weakens, err := s.weakeningsIn(ctx, tx, in.ProjectID, in.Candidate)
	if err != nil {
		return ScopedTarget{}, err
	}
	if err := s.require(ctx, authorization{Operation: OperationSelect, ProjectID: in.ProjectID, Actor: in.Actor, Weakens: len(weakens) > 0}); err != nil {
		return ScopedTarget{}, err
	}
	required, err := requiredCapabilitiesIn(ctx, tx, in.Candidate)
	if err != nil {
		return ScopedTarget{}, err
	}
	if err := s.requireCapabilities(ctx, tx, in.ProjectID, required); err != nil {
		return ScopedTarget{}, err
	}
	return recordTargetIn(ctx, tx, in, s.adoptions.now())
}

// RecordCurrent records the live union at the next revision, or returns the
// current receipt when unchanged. Resolution runs under Advance's project lock.
func (s *Service) RecordCurrent(ctx context.Context, projectID ids.ID, kind AdoptionKind, reason, by string) (*RecordedRevision, error) {
	return s.Advance(ctx, projectID, kind, reason, by, func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		return s.UnionIn(ctx, tx, projectID, prior)
	})
}
