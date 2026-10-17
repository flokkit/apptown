package workcommands

import (
	"context"
	"database/sql"
	"errors"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
)

// pinRunIntegrations appends integration.resolved events for the bindings a
// run will use: executor/model/secrets always; scm when the project is
// repo-backed. The configHash is the identity of "what was wired" for this
// run's receipts.
func (a *Preparation) pinRunIntegrations(ctx context.Context, emit func(kind string, payload any) error, projectID ids.ID) error {
	caps := []string{"secrets", "executor", "model", "deploy"}
	branchMeta, branchErr := project.ByIDIn(ctx, a.db, projectID)
	branch := branchMeta.DefaultBranch
	if err := branchErr; err != nil {
		return err
	}
	if branch != "" {
		caps = append([]string{"scm"}, caps...)
	}
	for _, capability := range caps {
		b, err := integration.Selected(ctx, a.db, capability, "")
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if err := emit("integration.resolved", map[string]any{
			"capability": capability, "binding": b.Name, "provider": b.Provider,
			"version": b.Version, "configHash": deploy.ConfigHash(b.ConfigJSON),
		}); err != nil {
			return err
		}
	}
	return nil
}
