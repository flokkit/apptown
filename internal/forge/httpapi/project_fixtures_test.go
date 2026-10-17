package httpapi

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

func httpTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
