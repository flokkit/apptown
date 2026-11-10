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
		if !s.hosts.readsDockerHost(t) {
			return inventoryObservation{Unknown: true, Unreported: true}, nil
		}
		r, err := snapshots.reading(ctx, t)
		if err != nil {
			return inventoryObservation{Unknown: true}, err
		}
		return snapshots.inventory(r)
	}
	return sources
}

// InventoryEvidence is a target's passive inventory answer: the last list
// read, with its age and coverage. Invalidated says the list was read before
// the latest act on the target; it is still the last list, and Missing holds
// only the absences that list's own complete reading witnessed.
type InventoryEvidence struct {
	Items      []deploy.Resource
	Shared     []SharedResource
	Missing    []deploy.Resource
	Reason     string // why the list is not current: a failure, or its age
	ObservedAt time.Time
	// Unknown: there is no list to show; Invalidated: the list predates an act.
	Unknown, Invalidated bool
	TookSeconds          float64 // the source lap's duration
}

// InventoryLive is the passive infrastructure view with source age and coverage.
func (s *Service) InventoryLive(ctx context.Context, t *Target) InventoryEvidence {
	r, err := s.InventoryFor(ctx, t)
	return evidenceOf(r, err)
}

func evidenceOf(r inventoryObservation, err error) InventoryEvidence {
	if err != nil {
		return InventoryEvidence{Reason: err.Error(), Unknown: true}
	}
	if r.Reason == "" && r.Stale {
		r.Reason = "Inventory reading is out of date"
	}
	out := InventoryEvidence{Reason: r.Reason, ObservedAt: r.ObservedAt, Unknown: r.Unknown, Invalidated: r.Invalidated, TookSeconds: r.TookSeconds}
	if !r.Unknown {
		out.Items, out.Shared, out.Missing = r.Items, r.Shared, r.Missing
	}
	return out
}
