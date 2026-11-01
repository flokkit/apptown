package workflow

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestChangeResourceRequiresItsCurrentProject(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := workflowTestProject(t, db, "scope", "Scope", "")
	ch := &Change{ID: idstest.N(90), ProjectID: pid}
	projects := project.New(db, nil)
	got, err := ChangeResource(t.Context(), projects, ch)
	if err != nil || got.Type != "change" || got.ID != ch.ID.String() || got.ChangeID != ch.ID || got.ProjectID != pid || got.ProjectName != "Scope" {
		t.Fatalf("change resource: %+v %v", got, err)
	}
	restore := dbtest.NextStatementFailure(db, "-- name: ProjectByID :one")
	got, err = ChangeResource(t.Context(), projects, ch)
	restore()
	if fault.KindOf(err) != fault.Unavailable || got != (access.Resource{}) {
		t.Fatalf("unreadable project became scope: %+v %v", got, err)
	}
	ch.ProjectID = idstest.N(99999)
	got, err = ChangeResource(t.Context(), projects, ch)
	if fault.KindOf(err) != fault.NotFound || got != (access.Resource{}) {
		t.Fatalf("missing project became scope: %+v %v", got, err)
	}
}
