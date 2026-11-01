// Package project owns the project record: its identity, its display
// words, the group that owns it, its release domain and its model profile.
// Adoption owns the project's selected ways of working and drift.
// AppTown keeps the doors and the acts that
// span domains (creation composes ownership and the first adoption
// revision; deletion purges every domain's rows). The package never
// imports internal/apptown.
package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// Project is one row of projects. The fields after CreatedAt are facts
// other domains read; they are not part of the project's API shape.
type Project struct {
	ID                         ids.ID `json:"id"`
	Slug                       string `json:"slug"`
	Name                       string `json:"name"`
	DefaultBranch              string `json:"defaultBranch"` // non-empty = repo-backed
	Namespace                  string `json:"namespace"`     // display grouping on the Projects page
	Icon                       string `json:"icon"`          // an emoji — the project's face on cards
	Blurb                      string `json:"blurb"`         // a short phrase: what this project is
	BlurbSource                string `json:"blurbSource"`
	CreatedAt                  string `json:"createdAt"`
	RequireIndependentApproval bool   `json:"requireIndependentApproval"`
	// SettingsRevision advances with every write of the name, slug, namespace,
	// icon, blurb or its source. Generated text also advances it. An edit
	// sends the revision it started from.
	SettingsRevision int64 `json:"settingsRevision"`

	// LastWorkedAt is the caller’s latest work, populated only by Browse with Mine set.
	LastWorkedAt     *time.Time `json:"-"`
	OwnerGroup       string     `json:"-"`
	DefaultApprovers string     `json:"-"`
	ReleaseDomain    string     `json:"-"`
	ModelProfile     string     `json:"-"`
	// Purpose is what the project is for, fixed at creation (purpose.go).
	// The column's CHECK holds it to the known purposes.
	Purpose Purpose `json:"-"`
	// ServingLabel names existing Docker routes and historical release previews.
	// It is captured at creation and never follows settings edits.
	ServingLabel string `json:"-"`
}

var (
	slugRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	ErrSlug       = fault.New(fault.Invalid, "slug must be lowercase letters, digits and dashes, starting with a letter or digit")
	ErrSlugTaken  = fault.New(fault.Conflict, "that project address has already been assigned; choose another")
	errNotDeleted = errors.New("the project row was not deleted")
)

// SlugShape is the shape a slug must have, for messages.
const SlugShape = `^[a-z0-9][a-z0-9-]*$`

func ValidSlug(slug string) bool { return slugRe.MatchString(slug) }

func record(r projectsql.Project) Project {
	return Project{ID: r.ID, Slug: r.Slug, Name: r.Name, DefaultBranch: r.DefaultBranch, Namespace: r.Namespace, Icon: r.Icon, Blurb: r.Blurb, BlurbSource: r.BlurbSource,
		CreatedAt:  r.CreatedAt.Format(time.RFC3339Nano),
		OwnerGroup: r.OwnerGroup, DefaultApprovers: r.DefaultApprovers, ReleaseDomain: r.ReleaseDomain, ModelProfile: r.ModelProfile, RequireIndependentApproval: r.RequireIndependentApproval, SettingsRevision: r.SettingsRevision, Purpose: Purpose(r.Purpose), ServingLabel: r.ServingLabel}
}

// ByIDIn reads a project through q; absence is the query's error.
func ByIDIn(ctx context.Context, q database.Queryer, id ids.ID) (Project, error) {
	return readID(ctx, q, id)
}

func readID(ctx context.Context, q database.Queryer, id ids.ID) (Project, error) {
	r, err := projectsql.New(q.SQL()).ProjectByID(ctx, id)
	if err != nil {
		return Project{}, database.Missing(database.QueryError(err, "ProjectByID"))
	}
	return record(r), nil
}

// SlugsIn is every project's slug, ordered.
func SlugsIn(ctx context.Context, q database.Queryer) ([]string, error) {
	return projectsql.New(q.SQL()).ProjectSlugs(ctx)
}

// Projects is the supported API over the project record.
type Projects struct {
	db        *database.DB
	authority access.ReadAuthorizer
	clock     func() time.Time
}

// New is the project record on the real clock; AppTown's own is
// NewWithClock's.
func New(db *database.DB, authority access.Authority) *Projects {
	return NewWithClock(db, authority, nil)
}

// NewWithClock is the project record whose writes (contract snapshots,
// deletion attempts) are stamped by now, AppTown's clock (the real clock
// when nil).
func NewWithClock(db *database.DB, authority access.Authority, now func() time.Time) *Projects {
	s := &Projects{db: db, clock: now}
	s.authority = access.Checked{Authority: authority, Projects: s}
	return s
}

func (s *Projects) now() time.Time { return clock.Or(s.clock)().UTC() }

func (s *Projects) ByID(ctx context.Context, id ids.ID) (Project, error) {
	return ByIDIn(ctx, s.db, id)
}

func (s *Projects) BySlug(ctx context.Context, slug string) (Project, error) {
	r, err := projectsql.New(s.db.SQL()).ProjectBySlug(ctx, slug)
	if err != nil {
		return Project{}, database.Missing(database.QueryError(err, "ProjectBySlug"))
	}
	return record(r), nil
}

// ByServingLabel resolves only the immutable serving namespace. A project's
// AppTown address never authorizes another application hostname or certificate.
func (s *Projects) ByServingLabel(ctx context.Context, label string) (Project, error) {
	r, err := queries(s.db).ProjectByServingLabel(ctx, label)
	if err != nil {
		return Project{}, database.Missing(database.QueryError(err, "ProjectByServingLabel"))
	}
	return record(r), nil
}

// Spec is what a project is made from.
type Spec struct {
	// ID is the id the caller chose for the project, as a client may choose
	// a change's (test fixtures name their projects in creation order); Nil
	// mints one.
	ID                                 ids.ID
	Slug, Name, Namespace, Blurb, Icon string
	OwnerGroup, DefaultApprovers       string
	// Purpose is written with the row and never changed; empty is Ordinary.
	Purpose Purpose
}

// CreateAt inserts the record inside the caller's transaction, which
// composes the rest of creation (ownership, the first adoption revision,
// the receipt), created at at, the caller's clock. The slug must have the
// shape; a taken slug is ErrSlugTaken.
func CreateAt(ctx context.Context, tx *database.Tx, at time.Time, in Spec) (ids.ID, error) {
	if !slugRe.MatchString(in.Slug) {
		return ids.Nil, fmt.Errorf("%w (%s)", ErrSlug, SlugShape)
	}
	if in.Name == "" {
		in.Name = in.Slug
	}
	if in.Purpose == "" {
		in.Purpose = Ordinary
	}
	if _, err := purposeOf(string(in.Purpose)); err != nil {
		return ids.Nil, err
	}
	id := in.ID
	if id.IsZero() {
		id = ids.New(at.UTC())
	}
	if err := AddressFreeIn(ctx, tx, id, in.Slug); err != nil {
		return ids.Nil, err
	}
	source := "person"
	if in.Purpose.Descriptions() {
		source = "generated"
	}
	r, err := queries(tx).InsertProject(ctx, projectsql.InsertProjectParams{ID: id, Slug: in.Slug, Name: in.Name, Namespace: in.Namespace, Blurb: in.Blurb, BlurbSource: source, Icon: in.Icon, OwnerGroup: in.OwnerGroup, DefaultApprovers: in.DefaultApprovers, CreatedAt: at.UTC(), Purpose: string(in.Purpose), ServingLabel: in.Slug})
	if slugTaken(err) {
		return ids.Nil, ErrSlugTaken
	}
	if err != nil {
		return ids.Nil, err
	}
	return r.ID, nil
}

// Remove deletes the row once every domain has purged its own; a row
// that was not there is ErrNotDeleted, never a silent success.
func (s *Projects) Remove(ctx context.Context, id ids.ID) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error { return RemoveIn(ctx, tx, id, "", nil, s.now()) })
}

// RemoveIn deletes the project row inside the caller's transaction, last
// of every domain's participant, with the `project.deleted` receipt when
// a person is named, stamped at, the caller's clock. A row that was not
// there is ErrNotDeleted, never a silent success.
func RemoveIn(ctx context.Context, tx *database.Tx, id ids.ID, who string, payload map[string]any, at time.Time) error {
	r, err := queries(tx).ProjectByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotDeleted
	}
	if err != nil {
		return err
	}
	n, err := queries(tx).DeleteProject(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotDeleted
	}
	if who == "" {
		return nil
	}
	return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "project.deleted", r.Slug, payload)
}

// All is every project, grouped by namespace then name, as the Projects
// page and the MCP list order them.
func (s *Projects) All(ctx context.Context) ([]Project, error) {
	rows, err := queries(s.db).ProjectsByNamespaceAndName(ctx)
	if err != nil {
		return []Project{}, err
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, record(r))
	}
	return out, nil
}

func (s *Projects) SetModelProfile(ctx context.Context, id ids.ID, profile string) error {
	return queries(s.db).SetProjectModelProfile(ctx, projectsql.SetProjectModelProfileParams{ID: id, ModelProfile: profile})
}
func (s *Projects) SetDefaultBranch(ctx context.Context, id ids.ID, branch string) error {
	return queries(s.db).SetProjectDefaultBranch(ctx, projectsql.SetProjectDefaultBranchParams{ID: id, DefaultBranch: branch})
}

// SetDefaultBranchIn records a project's default branch in the caller's
// transaction, so a repository's setup and its first recorded main settle together.
func SetDefaultBranchIn(ctx context.Context, tx *database.Tx, id ids.ID, branch string) error {
	return queries(tx).SetProjectDefaultBranch(ctx, projectsql.SetProjectDefaultBranchParams{ID: id, DefaultBranch: branch})
}

// Contracts is the snapshots of a project's native declaration, kept as evidence.
func (s *Projects) Contracts() *Contracts { return &Contracts{db: s.db, clock: s.clock} }

// Readable returns the named project once current policy lets the verified
// caller read it. A missing project and a refusal remain different errors.
func (s *Projects) Readable(ctx context.Context, slug string) (Project, error) {
	p, err := s.BySlug(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	res := access.Resource{Type: "project", ID: p.ID.String(), ProjectID: p.ID}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: res, Context: access.Context{Route: "project"}}); err != nil {
		return Project{}, err
	}
	return p, nil
}

// Visible returns project rows authorized for the verified caller, including
// projects with no Work activity. IDs never come from a caller's permission claim.
func (s *Projects) Visible(ctx context.Context) ([]Project, error) {
	idList, err := s.authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return nil, err
	}
	rows, err := queries(s.db).VisibleProjects(ctx, idList)
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(rows))
	for _, row := range rows {
		out = append(out, record(row))
	}
	return out, nil
}

// slugTaken: the write broke a unique rule, which for a project's row is
// its address (its serving label is never rewritten and starts as the
// address).
func slugTaken(err error) bool {
	var state interface{ SQLState() string }
	return errors.As(err, &state) && state.SQLState() == "23505"
}

// OriginalRequest returns the unabridged creation request to a project reader.
func (s *Projects) OriginalRequest(ctx context.Context, id ids.ID) (string, error) {
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: id.String(), ProjectID: id}}); err != nil {
		return "", err
	}
	return audit.CreationRequest(ctx, s.db, id)
}
