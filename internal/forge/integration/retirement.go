package integration

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/integration/internal/integrationsql"
)

// RetireConnectorIn removes the unused target connector and records its receipt.
// Deployment holds its targets table lock and establishes non-use before calling;
// this participant commits with that same retirement transaction.
func RetireConnectorIn(ctx context.Context, tx *database.Tx, provider, capability, targetName, who string, at time.Time) error {
	counts, err := integrationsql.New(tx.SQL()).RetireConnector(ctx, integrationsql.RetireConnectorParams{Name: provider, Capability: capability})
	if err != nil {
		return err
	}
	selected, removed := counts.SelectedCount, counts.RemovedCount
	if selected != removed {
		return fmt.Errorf("target %s connector was not removed", targetName)
	}
	if removed > 0 {
		return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "integration.deleted", provider, map[string]any{"with": targetName})
	}
	return nil
}
