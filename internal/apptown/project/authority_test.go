package project

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAuthorityResourceUsesCurrentProject(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	var id ids.ID
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		id, err = CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "p", Name: "P", OwnerGroup: "kids"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	projects := New(db, nil)
	got, err := projects.AuthorityResource(context.Background(), id)
	if err != nil || got.ProjectID != id || got.ProjectName != "P" || got.Group != "kids" || got.Type != "project" {
		t.Fatalf("project facts: %+v %v", got, err)
	}
	if _, err := projects.AuthorityResource(context.Background(), idstest.N(9999)); !errors.Is(err, sql.ErrNoRows) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("missing project: %v", err)
	}
	clear := dbtest.NextStatementFailure(db, "-- name: ProjectByID :one")
	defer clear()
	if got, err := projects.AuthorityResource(context.Background(), id); fault.KindOf(err) != fault.Unavailable || got != (access.Resource{}) {
		t.Fatalf("failed read reused prior facts: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := projects.AuthorityResource(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
