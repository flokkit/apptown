package apptown

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
)

// Deployment calls this adapter only after proving non-use under its table lock.
type targetConnectors struct{}

func (c targetConnectors) RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, targetName, who string, at time.Time) error {
	return integration.RetireConnectorIn(ctx, tx, provider, digitalocean.CapCloudAccount, targetName, who, at)
}
