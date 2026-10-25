// Package projectlifecycle owns project setup, adoption settlement and the
// namespace reservations shared by creation, rename and purge. HTTP, MCP and
// background completion use the same commands; no transport is retained.
package projectlifecycle

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

var (
	ErrInvalidSlug = fault.New(fault.Invalid, fmt.Sprintf("slug must match %s", project.SlugShape))
	ErrNameBusy    = fault.New(fault.Conflict, "project name is busy")
)

type nameBusy struct{ slug string }

func (e *nameBusy) Error() string {
	return fmt.Sprintf("project name %q is busy; retry after its current operation", e.slug)
}
func (e *nameBusy) Unwrap() error { return ErrNameBusy }

// Draft is the project declaration and receipt attribution after authorization.
type Draft struct {
	RequestID        ids.ID
	AutomaticAddress bool
	Setup            *CreationSetup
	// ID is the id the caller chose (project.Spec.ID); Nil mints one.
	ID                                               ids.ID
	Slug, Name, Namespace, Blurb, Icon, Group, Actor string
	// Pack is the pack the caller will apply next, when it names one. It
	// is not applied here; Create refuses one the instance does not offer
	// before making anything, so a failed `apptown new --pack` leaves nothing.
	Pack string
}

// Publisher publishes a committed branch with the established best-effort receipt policy.
type Publisher interface {
	PushBranch(ctx context.Context, projectID ids.ID, repoDir, branch string)
}
type Dependencies struct {
	Identity     *identity.Service
	Adoption     *adoption.Service
	Projects     *project.Projects
	Ways         *wow.Service
	Repositories *scm.RepositoryLocks
	Publisher    Publisher
	Changes      Changes
	// Secrets supplies a git credential an administrator names when a
	// project starts from an https repository.
	Secrets Secrets
}
type Config struct {
	DataDir    string
	Extensions []string
	Seeding    Seeding
	// Resolve looks a source host up when a project starts from an https
	// repository; nil uses the system resolver. Tests supply one.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Now is AppTown's clock, by which who administers is judged when a
	// project is given its owning group; nil is the real one.
	Now func() time.Time
}
type Service struct {
	identity     *identity.Service
	now          func() time.Time
	ways         *wow.Service
	repositories *scm.RepositoryLocks
	publisher    Publisher
	changes      Changes
	extensions   []string
	secrets      Secrets
	seeding      Seeding
	resolve      func(ctx context.Context, host string) ([]netip.Addr, error)

	projects         *project.Projects
	dataDir          string
	db               *database.DB
	adoption         *adoption.Service
	namesMu          sync.Mutex
	projectNamesBusy map[string]bool
}

func New(db *database.DB, deps Dependencies, cfg Config) *Service {
	return &Service{db: db, identity: deps.Identity, adoption: deps.Adoption, projects: deps.Projects, ways: deps.Ways, repositories: deps.Repositories, publisher: deps.Publisher, changes: deps.Changes, secrets: deps.Secrets, seeding: cfg.Seeding, resolve: cfg.Resolve, dataDir: cfg.DataDir, extensions: slices.Clone(cfg.Extensions), projectNamesBusy: make(map[string]bool), now: clock.Or(cfg.Now)}
}

// Create is the application command behind the wizard, `apptown new` and the
// MCP's create_project: the record, its owning group (the first
// administrator's personal group when the draft names none), the creator's
// owner role. The repo and the pack are separate
// acts the caller may add; a pack the draft names must be offered.
func (s *Service) Create(ctx context.Context, in Draft) (*project.Project, error) {
	if !in.RequestID.IsZero() {
		return s.createRequested(ctx, in)
	}
	if in.AutomaticAddress || in.Setup != nil {
		return nil, project.ErrRequestRequired
	}
	group := in.Group
	if !project.ValidSlug(in.Slug) {
		return nil, ErrInvalidSlug
	}
	if in.Pack != "" {
		if _, err := s.OfferedPack(ctx, in.Pack); err != nil {
			return nil, err
		}
	}
	release, err := s.ReserveNames(in.Slug)
	if err != nil {
		return nil, err
	}
	defer release()
	// Blurb doubles as the project's remembered intent: the wizard's "what is
	// this" sentence, reused by every Build-the-first-version door.
	receipt, err := s.adoption.Union(ctx, ids.Nil, nil)
	if err != nil {
		return nil, err
	}
	var p project.Project
	now := s.now()
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		p, err = s.createIn(ctx, tx, in, group, receipt, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// createIn composes the same local creation participants for ordinary and replayable commands.
func (s *Service) createIn(ctx context.Context, tx *database.Tx, in Draft, group string, receipt adoption.Receipt, now time.Time) (project.Project, error) {
	var out project.Project
	if group == "" {
		var err error
		group, err = identity.SovereignGroupIn(ctx, tx, now)
		if err != nil {
			return out, err
		}
	}
	if group != "" {
		if err := identity.RequireLiveGroupIn(ctx, tx, group); err != nil {
			return out, err
		}
	}
	id, err := project.CreateAt(ctx, tx, now, project.Spec{ID: in.ID, Slug: in.Slug, Name: in.Name, Namespace: in.Namespace, Blurb: in.Blurb, Icon: in.Icon, OwnerGroup: group, DefaultApprovers: "role:" + access.RoleProjectApprover + ",role:" + access.RoleProjectOwner})
	if err != nil {
		return out, err
	}
	if err := adoption.InitializeIn(ctx, tx, id, 0, "", ""); err != nil {
		return out, err
	}
	if err := adoption.RecordIn(ctx, tx, id, 1, adoption.AdoptionCreate, "project created", receipt, in.Actor, now); err != nil {
		return out, err
	}
	opener, err := identity.ActorIn(ctx, tx, in.Actor)
	if err != nil {
		return out, err
	}
	creator := in.Actor
	if !opener.Principal.IsZero() {
		creator, err = identity.PersonNameIn(ctx, tx, opener.Principal)
		if err != nil {
			return out, err
		}
		if creator == "" {
			return out, access.ErrPrincipalRequired
		}
	}
	if err := identity.ProjectOwnershipIn(ctx, tx, id, creator, now); err != nil {
		return out, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, in.Actor, id, "project.created", in.Slug, map[string]any{"ownerGroup": group}); err != nil {
		return out, err
	}
	return project.ByIDIn(ctx, tx, id)
}

// OfferedPack is the pack a new project may be given by this name: one the
// instance offers (enabled, not the platform tier), as the creation wizard
// lists them. Any other name is adoption.ErrPackNotOffered.
func (s *Service) OfferedPack(ctx context.Context, name string) (*wow.Pack, error) {
	offered, err := s.ways.OfferedPacks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range offered {
		if offered[i].Name == name {
			return s.ways.Pack(ctx, name)
		}
	}
	return nil, fmt.Errorf("%w: %s is not offered to new projects", adoption.ErrPackNotOffered, name)
}

// Namespace reservations prevent a replacement project from acquiring paths
// while the deleted project's post-commit cleanup still owns them. They never
// hold the mutex across SQL, git or Docker operations.
func (s *Service) ReserveNames(slugs ...string) (func(), error) {
	slugs = slices.Clone(slugs)
	slices.Sort(slugs)
	slugs = slices.Compact(slugs)
	s.namesMu.Lock()
	defer s.namesMu.Unlock()
	for _, slug := range slugs {
		if s.projectNamesBusy[slug] {
			return nil, &nameBusy{slug}
		}
	}
	for _, slug := range slugs {
		s.projectNamesBusy[slug] = true
	}
	return func() {
		s.namesMu.Lock()
		for _, slug := range slugs {
			delete(s.projectNamesBusy, slug)
		}
		s.namesMu.Unlock()
	}, nil
}

// ConsoleAdmission temporarily prevents chats opening or becoming active on a
// project being deleted. The namespace remains reserved for the entire admission
// attempt, without holding its mutex while asking the console owner.
type ConsoleAdmission interface {
	Block(key, value string) (func(), error)
}
