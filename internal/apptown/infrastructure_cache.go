package apptown

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
)

type inventoryRefresh struct {
	targets   *deployment.Targets
	providers *deployment.Providers
	inventory *deployment.Inventory
	cache     *deployment.InventoryReadings
}

func (w inventoryRefresh) Run(ctx context.Context, key string, args map[string]any) (map[string]any, error) {
	if target, ok := args["target"].(string); ok {
		key = target
	}
	t, err := w.targets.ByName(ctx, key)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			err = jobs.Permanent(err)
		}
		return nil, err
	}
	identity := inventoryTargetIdentity(t)
	generation, err := w.cache.Start(ctx, t)
	if err != nil {
		return nil, err
	}
	inv, err := w.providers.TargetInventory(t)
	if errors.Is(err, deploy.ErrNoInventory) {
		// Nothing to ask: the kind does not report what it holds.
		return map[string]any{"unreported": true}, nil
	}
	var items []deploy.Resource
	var shared []deployment.SharedResource
	if err == nil {
		items, err = inv.Inventory(ctx)
	}
	if err == nil {
		shared, err = w.inventory.Shared(ctx, items)
	}
	if err == nil {
		current, readErr := w.targets.ByID(ctx, t.ID)
		if readErr != nil {
			err = readErr
		} else if inventoryTargetIdentity(current) != identity {
			err = errors.New("inventory target changed during observation")
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, ctx.Err()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	var recorded []deploy.Resource
	if err == nil {
		recorded, err = w.inventory.Recorded(ctx, t)
	}
	if err := w.cache.Finish(ctx, t, generation, items, shared, recorded, err); err != nil {
		return nil, err
	}
	return map[string]any{"target": t.DisplayLabel, "resources": len(items)}, nil
}

func inventoryTargetIdentity(t *deployment.Target) string {
	return fmt.Sprintf("%s/%d", t.ID, t.SettingsRevision)
}
