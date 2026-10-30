package projectlifecycle

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

type ownerFixture struct {
	db        *database.DB
	identity  *identity.Service
	ways      *wow.Service
	adoption  *adoption.Service
	lifecycle *Service
}

func newOwnerFixture(t *testing.T, syncStock bool) ownerFixture {
	t.Helper()
	return newOwnerFixtureOn(t, syncStock, nil)
}

// newOwnerFixtureOn is newOwnerFixture with the lifecycle on now.
func newOwnerFixtureOn(t *testing.T, syncStock bool, now func() time.Time) ownerFixture {
	t.Helper()
	db := databaseSuite.New(t)
	ids := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	ways := wow.New(db, wow.Dependencies{Stock: stock.FS, ToolLines: stock.Versions()})
	if syncStock {
		ways.SyncStock(t.Context())
	}
	projects := project.New(db, nil)
	adopt := adoption.New(db, adoption.Dependencies{Authority: access.Checked{Authority: identity.Scoped(ids, "adoption"), Projects: projects}, Ways: ways, Copies: project.Copies{}})
	locks := new(scm.RepositoryLocks)
	lifecycle := New(db, Dependencies{Identity: ids, Adoption: adopt, Projects: projects, Ways: ways, Repositories: locks, Publisher: noUpstream{}, Changes: workflow.NewChanges(db, access.Checked{Authority: identity.Scoped(ids, "work"), Projects: projects}, nil), Superseded: workflow.NewAbandonments(db, noAbandonEffects{}, locks)}, Config{DataDir: t.TempDir(), Extensions: []string{"lsp"}, Now: now})
	return ownerFixture{db: db, identity: ids, ways: ways, adoption: adopt, lifecycle: lifecycle}
}

func ownerCount(t *testing.T, db *database.DB, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := db.SQL().QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count %d, want %d: %v", got, want, err)
	}
}

// slugTaken asks the project owner whether any project holds slug.
func slugTaken(t *testing.T, db *database.DB, slug string) bool {
	t.Helper()
	slugs, err := project.SlugsIn(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(slugs, slug)
}

// lifecycleReceipts is every ledger line of exactly one kind. The read is
// bounded, so it refuses to answer when the bound is reached.
func lifecycleReceipts(t *testing.T, db *database.DB, kind string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, kind)
	}
	var out []audit.Entry
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// noUpstream is a Publisher for projects with no upstream remote: there is
// nothing to push to.
type noUpstream struct{}

func (noUpstream) PushBranch(context.Context, ids.ID, string, string) {}

// noAbandonEffects: these tests read what an abandonment records, not the
// workspace and run cleanup Work does afterwards.
type noAbandonEffects struct{}

func (noAbandonEffects) RecordAbandonIn(context.Context, *database.Tx, workflow.AbandonEffects) error {
	return nil
}
