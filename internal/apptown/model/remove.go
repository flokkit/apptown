package model

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// RemoveProjectIn removes a project's slot cells inside the caller's
// transaction; the instance's rows (project 0) are untouched.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	if projectID.IsZero() {
		return nil
	}
	_, err := queries(tx).RemoveProjectSlots(ctx, projectID)
	return err
}
