// Package apptown composes application operations, persistence, and HTTP APIs.
package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// audit records a project-level state change (workflow saved, ADR transitioned,
// guidance edited, …) — the reflection substrate run_events can't cover because
// those changes happen outside any run.
func (a *App) audit(ctx context.Context, actor string, projectID ids.ID, kind, subject string, payload any) {
	audit.At(a.cfg.clock.Now()).BestEffort(ctx, a.DB, actor, projectID, kind, subject, payload)
}

func (a *App) changeByID(ctx context.Context, id ids.ID) (*workflow.Change, error) {
	return workflow.ReadChangeIn(ctx, a.DB, id)
}
