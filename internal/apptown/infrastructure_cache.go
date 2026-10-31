package apptown

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/deploy"
)

// Inventory readings are collected only by durable background jobs. Readers keep
// the last successful answer and its age, including after a failed check.
type inventoryEntry struct {
	identity    string
	items       []deploy.Resource
	shared      []deployment.SharedResource
	err         string
	at, asked   time.Time
	generation  uint64
	invalidated bool
}

type inventoryCache struct {
	mu      sync.Mutex
	entries map[string]*inventoryEntry
	now     func() time.Time
}

// Live reads recorded evidence without requesting work.
func (c *inventoryCache) Live(ctx context.Context, t *deployment.Target) ([]deploy.Resource, []deployment.SharedResource, string, time.Time, bool) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err.Error(), time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[t.Name]
	if e == nil || e.identity != inventoryTargetIdentity(t) {
		return nil, nil, "", time.Time{}, false
	}
	out := c.observation(e)
	why := e.err
	if why == "" && !out.ObservedAt.IsZero() && (out.Stale || out.Invalidated) {
		why = "Inventory reading is out of date"
	}
	return out.Items, slices.Clone(e.shared), why, out.ObservedAt, e.invalidated
}

// Invalidate keeps the old reading visibly older than an AppTown act. Its
// generation also prevents an already running check from claiming freshness.
func (c *inventoryCache) Invalidate(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*inventoryEntry)
	}
	e := c.entries[name]
	if e == nil {
		e = &inventoryEntry{}
		c.entries[name] = e
	}
	e.generation++
	e.invalidated = true
}

type inventoryRefresh struct {
	targets   *deployment.Targets
	providers *deployment.Providers
	inventory *deployment.Inventory
	cache     *inventoryCache
}

// Run records the background check, including failures and verified placement.
func (w inventoryRefresh) Run(ctx context.Context, key string, args map[string]any) (map[string]any, error) {
	if target, ok := args["target"].(string); ok {
		key = target
	}
	t, err := w.targets.ByName(ctx, key)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			err = jobs.Permanent(err)
		}
		return nil, w.store(key, nil, err)
	}
	identity := inventoryTargetIdentity(t)
	w.cache.mu.Lock()
	if w.cache.entries == nil {
		w.cache.entries = make(map[string]*inventoryEntry)
	}
	e := w.cache.entries[key]
	if e == nil || e.identity != identity {
		e = &inventoryEntry{identity: identity}
		w.cache.entries[key] = e
	}
	generation := e.generation
	w.cache.mu.Unlock()
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
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err := w.storeGeneration(key, identity, generation, items, shared, err); err != nil {
		return nil, err
	}
	return map[string]any{"target": t.DisplayLabel, "resources": len(items)}, nil
}

func inventoryTargetIdentity(t *deployment.Target) string {
	return fmt.Sprintf("%s/%d", t.ID, t.SettingsRevision)
}

// SnapshotForTarget rejects evidence from an earlier target configuration.
func (c *inventoryCache) SnapshotForTarget(ctx context.Context, t *deployment.Target) (inventoryObservation, error) {
	if err := ctx.Err(); err != nil {
		return inventoryObservation{Unknown: true, Stale: true}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[t.Name]
	if e == nil || e.identity != inventoryTargetIdentity(t) {
		return inventoryObservation{Unknown: true, Stale: true}, nil
	}
	return c.observation(e), nil
}

// store writes an ask's outcome into the cache the page reads.
func (w inventoryRefresh) store(key string, items []deploy.Resource, err error) error {
	return w.storeGeneration(key, "", 0, items, nil, err)
}

func (w inventoryRefresh) storeGeneration(key, identity string, generation uint64, items []deploy.Resource, shared []deployment.SharedResource, err error) error {
	c := w.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*inventoryEntry)
	}
	e := c.entries[key]
	if e == nil {
		e = &inventoryEntry{}
		c.entries[key] = e
	}
	if identity != "" && e.identity != "" && e.identity != identity {
		return nil
	}
	if generation != e.generation {
		return err
	}
	if identity != "" {
		e.identity = identity
	}
	e.asked = clock.Or(c.now)()
	if err != nil {
		e.err = err.Error()
		return err
	}
	e.items, e.shared, e.err, e.at = items, shared, "", e.asked
	e.invalidated = false
	return nil
}

// inventoryObservation is evidence from the last completed provider read.
// Unknown and stale coverage must never establish that a resource is missing.
type inventoryObservation struct {
	Items       []deploy.Resource
	ObservedAt  time.Time
	AttemptedAt time.Time // Last completed attempt, including failures; zero while the first read is pending.
	Unknown     bool
	Invalidated bool
	Stale       bool
}

func (c *inventoryCache) observation(e *inventoryEntry) inventoryObservation {
	return inventoryObservation{Items: cloneInventory(e.items), ObservedAt: e.at, AttemptedAt: e.asked,
		Unknown: e.at.IsZero() || e.err != "", Invalidated: e.invalidated, Stale: e.at.IsZero() || clock.Or(c.now)().Sub(e.at) > 12*time.Minute}
}

func cloneInventory(items []deploy.Resource) []deploy.Resource {
	out := slices.Clone(items)
	for i := range out {
		out[i].Tags = slices.Clone(out[i].Tags)
		out[i].Hosts = slices.Clone(out[i].Hosts)
		if out[i].Retained != nil {
			note := *out[i].Retained
			out[i].Retained = &note
		}
	}
	return out
}
