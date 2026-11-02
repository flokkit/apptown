package deployment

import (
	"context"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// InventoryFor reads persisted inventory evidence for a target. Docker uses its
// host snapshot; other providers use their complete inventory readings. It does
// not contact a provider or request collection.
func (s *Service) InventoryFor(ctx context.Context, t *Target) (inventoryObservation, error) {
	if _, err := s.providers.TargetInventory(t); errors.Is(err, deploy.ErrNoInventory) {
		return inventoryObservation{Unknown: true, Unreported: true}, nil
	}
	read, ok := s.inventorySources[t.Kind]
	if !ok {
		return inventoryObservation{Unknown: true, Unreported: true}, nil
	}
	return read(ctx, t)
}

// newInventorySources binds each registered kind to its persisted evidence.
func (s *Service) newInventorySources() map[string]func(context.Context, *Target) (inventoryObservation, error) {
	readings := NewInventoryReadings(s.db, s.now)
	sources := make(map[string]func(context.Context, *Target) (inventoryObservation, error), len(s.providers.inventories))
	for kind := range s.providers.inventories {
		sources[kind] = readings.snapshotForTarget
	}
	snapshots := NewHostSnapshots(s.db, s.now)
	sources[KindDockerHost] = func(ctx context.Context, t *Target) (inventoryObservation, error) {
		r, err := snapshots.reading(ctx, t)
		if err != nil {
			return inventoryObservation{Unknown: true}, err
		}
		return snapshots.inventory(r)
	}
	return sources
}

// InventoryLive is the passive infrastructure view with source age and coverage.
func (s *Service) InventoryLive(ctx context.Context, t *Target) ([]deploy.Resource, []SharedResource, string, time.Time, bool, bool) {
	r, err := s.InventoryFor(ctx, t)
	if err != nil {
		return nil, nil, err.Error(), time.Time{}, false, true
	}
	if r.Reason == "" && (r.Stale || r.Invalidated) {
		r.Reason = "Inventory reading is out of date"
	}
	if r.Unknown {
		return nil, nil, r.Reason, r.ObservedAt, r.Invalidated, r.Unknown
	}
	return r.Items, r.Shared, r.Reason, r.ObservedAt, r.Invalidated, r.Unknown
}
