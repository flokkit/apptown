package workflow

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// A change-only fixture: valid creation and attribution, no run or narrative.
func changeEditFixture(t *testing.T, draft ChangeDraft) (*database.DB, *Change) {
	t.Helper()
	db := testDB(t)
	draft.ProjectID = changeTestProject(t, db, "changes")
	ch, err := NewCreator(db, nil).Create(context.Background(), draft, "creator")
	if err != nil {
		t.Fatal(err)
	}
	return db, ch
}

// testAccounts records each name as an account through the identity owner:
// Work decides only for a person with an enabled account, and refuses a
// name with none.
func testAccounts(t *testing.T, db *database.DB, names ...string) {
	t.Helper()
	ids := identity.New(db, identity.Config{})
	for _, name := range names {
		if _, err := ids.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
}

func changeTestProject(t *testing.T, db *database.DB, slug string) ids.ID {
	return workflowTestProject(t, db, slug, slug, "")
}

func workflowTestProject(t *testing.T, db *database.DB, slug, name, defaultBranch string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	if defaultBranch != "" {
		if err := project.New(db, nil).SetDefaultBranch(context.Background(), id, defaultBranch); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// revision is an edit's expected revision, for tests.
func revision(n int64) *int64 { return &n }

func editChangeAs(ctx context.Context, db *database.DB, who string, in ChangeEdit) (*Change, error) {
	s := newService(db, authorityFunc(func(context.Context, operationPermission) (string, error) { return who, nil }), Runtime{})
	return s.Edit(ctx, in)
}

func nameChangeForTest(t *testing.T, db *database.DB, ch *Change, words string) {
	t.Helper()
	if applied, err := (changeStore{db: db}).nameFromChat(context.Background(), ch.ProjectID, ch.ID, words, "chat"); err != nil || !applied {
		t.Fatalf("name change: %v %v", applied, err)
	}
}

func assertChangeTitle(t *testing.T, db *database.DB, id ids.ID, words string, owner TitleOwner) {
	t.Helper()
	row, err := workflowsql.New(db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	var got TitleOwner
	if err := got.Scan(row.TitleOwnership); err != nil {
		t.Fatal(err)
	}
	if row.Title != words || got != owner {
		t.Fatalf("title: %q owner %v, want %q owner %v", row.Title, got, words, owner)
	}
}

func changeReceipts(t *testing.T, db *database.DB, kind string, count int) []audit.Entry {
	t.Helper()
	rows, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind})
	if err != nil || len(rows) != count {
		t.Fatalf("%s receipts: %+v %v; want %d", kind, rows, err, count)
	}
	return rows
}
