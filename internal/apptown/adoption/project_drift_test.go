package adoption

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestProjectDriftAndRevisionSelection(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	as := New(db, Dependencies{})
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "coin-flip", OwnerGroup: "family"})
		if err != nil {
			return ids.Nil, err
		}
		return id, InitializeIn(t.Context(), tx, id, 1, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := as.Current(t.Context(), id); err != nil || current == nil || current.Revision != 1 {
		t.Fatalf("initial revision: %+v %v", current, err)
	}
	if err := as.NoteDrift(t.Context(), id, "moved outside the door"); err != nil {
		t.Fatal(err)
	}
	if got, err := as.Current(t.Context(), id); err != nil || got == nil || got.Drift == "" {
		t.Fatal("the drift note was not kept")
	}
	if err := as.ClearDrift(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	ids, _ := as.ProjectsWithRevisions(t.Context())
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("projects standing on a revision: %v", ids)
	}
}
