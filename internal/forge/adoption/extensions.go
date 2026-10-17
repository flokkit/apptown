package adoption

import (
	"context"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
)

// extensionStore are the opt-in pi extensions a project has turned on.
type extensionStore struct{ db *database.DB }

func newExtensions(db *database.DB) *extensionStore { return &extensionStore{db: db} }

// Set turns an extension on or off for a project.
func (e *extensionStore) Set(ctx context.Context, projectID ids.ID, name string, on bool) error {
	q := adoptionsql.New(e.db.SQL())
	if on {
		_, err := q.EnableProjectExtension(ctx, adoptionsql.EnableProjectExtensionParams{ProjectID: projectID, Name: name})
		return err
	}
	_, err := q.DisableProjectExtension(ctx, adoptionsql.DisableProjectExtensionParams{ProjectID: projectID, Name: name})
	return err
}
