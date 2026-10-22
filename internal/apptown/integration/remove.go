package integration

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/database"
)

// RemoveProjectIn removes a project's attachments inside the caller's
// transaction; the instance's bindings and service kinds are untouched.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := integrationsql.New(tx.SQL()).DeleteProjectIntegrations(ctx, projectID)
	return err
}
