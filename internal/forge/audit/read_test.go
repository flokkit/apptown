package audit_test

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

// A project's reading is its own lines and the instance's; a kind prefix
// is one family; the limit is bounded; payloads come back compact.
func TestRecentFilters(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "q", Name: "Q"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = db.InTx(ctx, func(tx *database.Tx) error {
		if err := audit.At(time.Now()).RecordIn(ctx, tx, "bls", pid, "secret.unbound", "TOKEN", map[string]any{"store": "internal"}); err != nil {
			return err
		}
		if err := audit.At(time.Now()).RecordIn(ctx, tx, "bls", other, "secret.unbound", "OTHER", nil); err != nil {
			return err
		}
		if err := audit.At(time.Now()).RecordIn(ctx, tx, "apptown-system", ids.Nil, "complaints.expired", "1", nil); err != nil {
			return err
		}
		return audit.At(time.Now()).RecordIn(ctx, tx, "bls", pid, "dependency.saved", "sso", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := audit.Recent(ctx, db, audit.Filter{})
	if err != nil || len(all) != 4 || all[0].Kind != "dependency.saved" {
		t.Fatalf("all: %v %+v", err, all)
	}
	mine, err := audit.Recent(ctx, db, audit.Filter{ProjectID: pid})
	if err != nil || len(mine) != 3 {
		t.Fatalf("a project reads its own and the instance's: %v %d", err, len(mine))
	}
	for _, e := range mine {
		if e.ProjectID == other {
			t.Fatal("another project's line leaked")
		}
	}
	if mine[0].ProjectSlug != "p" || mine[1].ProjectSlug != "" {
		t.Fatalf("slugs: %+v", mine)
	}
	secrets, err := audit.Recent(ctx, db, audit.Filter{KindPrefix: "secret."})
	if err != nil || len(secrets) != 2 || string(secrets[1].Payload) != `{"store":"internal"}` {
		t.Fatalf("kind family: %v %+v", err, secrets)
	}
	if one, _ := audit.Recent(ctx, db, audit.Filter{Limit: 1}); len(one) != 1 {
		t.Fatalf("limit: %d", len(one))
	}
	if capped, _ := audit.Recent(ctx, db, audit.Filter{Limit: audit.MaxLimit + 1}); len(capped) != 4 {
		t.Fatalf("a limit past the cap reads the default: %d", len(capped))
	}
	if bySubject, err := audit.Recent(ctx, db, audit.Filter{KindPrefix: "secret.", Subject: "TOKEN"}); err != nil || len(bySubject) != 1 || bySubject[0].Subject != "TOKEN" {
		t.Fatalf("subject filter: %v %+v", err, bySubject)
	}
	if none, err := audit.Recent(ctx, db, audit.Filter{KindPrefix: "nothing."}); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("empty reading: %v %v", err, none)
	}
}
