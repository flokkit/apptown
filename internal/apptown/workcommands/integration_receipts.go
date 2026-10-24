package workcommands

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
)

// pinRunIntegrations appends integration.resolved events for the instance's
// wiring when a run is admitted: the executor, model and deploy bindings,
// scm when the project is repo-backed, and every secrets store available
// then (the built-in store; Infisical when set up). They record what was
// wired, not what the run read: a step's STEP_ values come from
// secrets.env, an app's values through its own bindings, and an eval copy
// gets none. The configHash is the identity of "what was wired".
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
		var bs []integration.Binding
		if capability == integration.CapSecrets {
			if a.stores == nil {
				continue
			}
			all, err := integration.ForCapability(ctx, a.db, capability)
			if err != nil {
				return err
			}
			available, err := a.stores.AvailableProviders()
			if err != nil {
				return err
			}
			bs = integration.WithProviders(all, available)
		} else {
			b, err := integration.Selected(ctx, a.db, capability, "")
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			bs = []integration.Binding{b}
		}
		for _, b := range bs {
			if err := emit("integration.resolved", map[string]any{
				"capability": capability, "binding": b.Name, "provider": b.Provider,
				"version": b.Version, "configHash": deploy.ConfigHash(b.ConfigJSON),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
