package integration

import (
	"context"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration/internal/integrationsql"
)

// RemoveProjectIn removes a project's attachments inside the caller's
// transaction; the instance's bindings and service kinds are untouched.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := integrationsql.New(tx.SQL()).DeleteProjectIntegrations(ctx, projectID)
	return err
}
