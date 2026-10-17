package forge

import (
	"context"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/forge/integration"
)

// Deployment calls this adapter only after proving non-use under its table lock.
type targetConnectors struct{}

func (c targetConnectors) RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, targetName, who string, at time.Time) error {
	return integration.RetireConnectorIn(ctx, tx, provider, digitalocean.CapCloudAccount, targetName, who, at)
}
