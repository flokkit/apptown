package apptown

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/deploy"
)

// The inventory cache: a provider's list is slow to ask for (several
// round-trips across the world) and changes rarely, so the last answer is
// served at once and refreshed behind the page when it is older than
// inventoryTTL. Two things keep it from lying: AppTown's own records are
// never cached (they are read fresh on every request and laid over the
// cached list), and every act AppTown takes at a provider — a release
// recorded, a need fulfilled, a deployment torn down — drops the cache,
// so the next visit asks again. The page says how old the answer is and
// offers a refresh.

var inventoryTTL = time.Minute

type inventoryEntry struct {
	identity string
	items    []deploy.Resource
	err      string
	// at is when items were read: the last ask that answered. A failed ask
	// keeps it, so a list kept under an error shows its own age, never the
	// failed ask's time; zero until an ask has answered.
	at time.Time
	// asked is when the last ask finished, answered or not: what ages the
	// entry for a refresh, so a host that keeps failing is asked again once
	// a TTL, not on every visit.
	asked      time.Time
	refreshing bool
	// direct distinguishes a running synchronous provider call from an abandoned job.
	direct    bool
	requested time.Time
	// dropped: AppTown acted at the provider while an ask was on its
	// way, so that answer may be older than the act; the next visit asks
	// again. The answer keeps its own time: zeroing it made a host whose
	// list was still being read show as unreachable, its records missing.
	dropped bool
	// invalidated keeps old evidence stale until a post-act read succeeds.
	invalidated bool
}

type inventoryCache struct {
	mu      sync.Mutex
	entries map[string]*inventoryEntry
	now     func() time.Time // AppTown's clock, which ages the lists; nil is the real one
}

// Live returns the provider's list for a target: from the cache
// when it has one (refreshing behind the page if stale), or asked for now
// when there is none or a refresh was demanded. Returns the list, its
// error if the ask failed, when it was asked, and whether a refresh is
// under way.
func (c *inventoryCache) Live(ctx context.Context, queue *jobs.Queue, name string, inv deploy.Inventorier, force bool) ([]deploy.Resource, string, time.Time, bool) {
	c.mu.Lock()
	e := c.entries[name]
	if e != nil && !force {
		stale := e.dropped || clock.Or(c.now)().Sub(e.asked) > inventoryTTL
		if stale && !e.refreshing {
			e.refreshing, e.dropped = true, false
			// The refresh outlives the visit that asked for it.
			go c.refresh(context.WithoutCancel(ctx), queue, name)
		}
		items, err, at, refreshing := e.items, e.err, e.at, e.refreshing
		c.mu.Unlock()
		return items, err, at, refreshing
	}
	if e != nil && e.refreshing {
		// A refresh is already on its way; serve what there is rather
		// than asking twice.
		items, err, at := e.items, e.err, e.at
		c.mu.Unlock()
		return items, err, at, true
	}
	if e == nil {
		e = &inventoryEntry{}
		c.entries[name] = e
	}
	e.refreshing, e.dropped, e.direct = true, false, true
	c.mu.Unlock()
	items, err := inv.Inventory(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	e.refreshing, e.direct = false, false
	e.asked = clock.Or(c.now)()
	if err != nil {
		e.err = err.Error()
		// Keep the last good list under the error, with its own time, so
		// the page still shows something and says how old it is.
		return e.items, e.err, e.at, false
	}
	e.items, e.err, e.at = items, "", e.asked
	e.invalidated = e.dropped
	return e.items, "", e.at, false
}

// refresh asks for the target's inventory as a job; the cache
// entry stays "refreshing" until the job writes the answer.
func (c *inventoryCache) refresh(ctx context.Context, queue *jobs.Queue, name string) {
	if _, err := queue.Enqueue(ctx, "inventory", name, nil, time.Time{}); err != nil {
		c.mu.Lock()
		if e := c.entries[name]; e != nil {
			e.refreshing = false
		}
		c.mu.Unlock()
		log.Printf("infrastructure: asking for %s's inventory: %v", name, err)
	}
}

// Invalidate drops every cached list: AppTown changed something at
// a provider, so the next visit asks again.
func (c *inventoryCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, e := range c.entries {
		if !e.refreshing {
			delete(c.entries, name)
		} else {
			// Let the refresh land, but mark it old so the one after asks again.
			e.dropped = true
			e.invalidated = true
		}
	}
}

type inventoryRefresh struct {
	targets   *deployment.Targets
	providers *deployment.Providers
	cache     *inventoryCache
}

// ask reads the target's inventory. A target that cannot be read is an
// answer too (an error), so the entry is never left "refreshing" forever.
func (w inventoryRefresh) Run(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	t, err := w.targets.ByName(ctx, key)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			err = jobs.Permanent(err)
		}
		return nil, w.store(key, nil, err)
	}
	identity := inventoryTargetIdentity(t)
	inv, err := w.providers.TargetInventory(t)
	var items []deploy.Resource
	if err == nil {
		items, err = inv.Inventory(ctx)
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
	if err := w.storeObserved(key, identity, items, err); err != nil {
		return nil, err
	}
	return map[string]any{"resources": len(items)}, nil
}

func inventoryTargetIdentity(t *deployment.Target) string {
	return fmt.Sprintf("%s/%d", t.ID, t.SettingsRevision)
}

// SnapshotForTarget binds the cache to current target configuration. A cached
// provider answer from an old address cannot establish missing resources here.
func (c *inventoryCache) SnapshotForTarget(ctx context.Context, queue inventoryObservationRequests, t *deployment.Target) (inventoryObservation, error) {
	identity := inventoryTargetIdentity(t)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*inventoryEntry)
	}
	if e := c.entries[t.Name]; e != nil && e.direct && e.identity != identity {
		c.mu.Unlock()
		return inventoryObservation{Unknown: true, Stale: true}, nil
	}
	if e := c.entries[t.Name]; e == nil || e.identity != identity {
		c.entries[t.Name] = &inventoryEntry{identity: identity}
	}
	c.mu.Unlock()
	out, err := c.Snapshot(ctx, queue, t.Name)
	c.mu.Lock()
	current := c.entries[t.Name]
	if current == nil || current.identity != identity {
		out = inventoryObservation{Unknown: true, Stale: true}
	}
	c.mu.Unlock()
	return out, err
}

// store writes an ask's outcome into the cache the page reads.
func (w inventoryRefresh) store(key string, items []deploy.Resource, err error) error {
	return w.storeObserved(key, "", items, err)
}

func (w inventoryRefresh) storeObserved(key, identity string, items []deploy.Resource, err error) error {
	c := w.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		e = &inventoryEntry{}
		c.entries[key] = e
	}
	if identity != "" && e.identity != "" && e.identity != identity {
		return nil
	}
	if identity != "" {
		e.identity = identity
	}
	e.refreshing = false
	e.asked = clock.Or(c.now)()
	if err != nil {
		e.err = err.Error()
		return err
	}
	e.items, e.err, e.at = items, "", e.asked
	e.invalidated = e.dropped
	return nil
}

// inventoryObservation is evidence from the last completed provider read.
// Unknown and stale coverage must never establish that a resource is missing.
type inventoryObservation struct {
	Items      []deploy.Resource
	ObservedAt time.Time
	Unknown    bool
	Stale      bool
}

type inventoryObservationRequests interface {
	RequestInventory(context.Context, string) error
}

// Snapshot never calls a provider. Cold and expired reads ask the existing
// durable inventory job to collect evidence. Requests are bounded even when a
// queued job is canceled; durable uniqueness also covers an already running job.
func (c *inventoryCache) Snapshot(ctx context.Context, queue inventoryObservationRequests, name string) (inventoryObservation, error) {
	if err := ctx.Err(); err != nil {
		return inventoryObservation{Unknown: true, Stale: true}, err
	}
	now := clock.Or(c.now)()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*inventoryEntry)
	}
	e := c.entries[name]
	if e == nil {
		e = &inventoryEntry{}
		c.entries[name] = e
	}
	due := e.asked.IsZero() || e.dropped || now.Sub(e.asked) >= inventoryTTL
	request := !e.direct && due && (e.requested.IsZero() || now.Sub(e.requested) >= inventoryTTL)
	out := inventoryObservation{Items: cloneInventory(e.items), ObservedAt: e.at, Unknown: e.at.IsZero() || e.err != "", Stale: e.dropped || e.invalidated || e.at.IsZero() || now.Sub(e.at) > 2*time.Minute}
	if request {
		e.requested = now
		if !e.refreshing {
			e.dropped = false
		}
		e.refreshing = true
	}
	c.mu.Unlock()
	if !request {
		return out, nil
	}
	var err error
	if queue == nil {
		err = errors.New("inventory observation queue unavailable")
	} else {
		err = queue.RequestInventory(ctx, name)
	}
	if err != nil {
		c.mu.Lock()
		e.err = err.Error()
		e.refreshing = false
		c.mu.Unlock()
		out.Unknown = true
	}
	return out, err
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
