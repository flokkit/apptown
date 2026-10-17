package verification

import (
	"context"
	"encoding/json"
	"fmt"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/workflow"
)

type StageScanners struct{}

// This named inventory participant reads through Work's existing transaction.
// It neither opens a second pool connection nor exposes scanner credentials.
func (StageScanners) SnapshotIn(ctx context.Context, db database.Queryer) ([]workflow.Scanner, error) {
	bindings, err := integration.ForCapability(ctx, db, ScannerCapability)
	if err != nil {
		return nil, err
	}
	var out []workflow.Scanner
	for _, b := range bindings {
		var m check.Scanner
		if err := json.Unmarshal([]byte(b.ConfigJSON), &m); err != nil {
			return nil, fmt.Errorf("scanner %s configuration: %w", b.Name, err)
		}
		if !m.Manual {
			out = append(out, workflow.Scanner{Name: b.Name, Target: m.Looks()})
		}
	}
	return out, nil
}
