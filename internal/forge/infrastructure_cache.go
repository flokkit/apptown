package forge

import (
	"context"
	"log"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/jobs"
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
	items []deploy.Resource
	err   string
	// at is when items were read: the last ask that answered. A failed ask
	// keeps it, so a list kept under an error shows its own age, never the
	// failed ask's time; zero until an ask has answered.
	at time.Time
	// asked is when the last ask finished, answered or not: what ages the
	// entry for a refresh, so a host that keeps failing is asked again once
	// a TTL, not on every visit.
	asked      time.Time
	refreshing bool
	// dropped: AppTown acted at the provider while an ask was on its
	// way, so that answer may be older than the act; the next visit asks
	// again. The answer keeps its own time: zeroing it made a host whose
	// list was still being read show as unreachable, its records missing.
	dropped bool
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
	e.refreshing, e.dropped = true, false
	c.mu.Unlock()
	items, err := inv.Inventory(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	e.refreshing = false
	e.asked = clock.Or(c.now)()
	if err != nil {
		e.err = err.Error()
		// Keep the last good list under the error, with its own time, so
		// the page still shows something and says how old it is.
		return e.items, e.err, e.at, false
	}
	e.items, e.err, e.at = items, "", e.asked
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
func (w inventoryRefresh) ask(ctx context.Context, key string) ([]deploy.Resource, error) {
	t, err := w.targets.ByName(ctx, key)
	if err != nil {
		// A target that is gone stays gone; a read that failed may not.
		if fault.KindOf(err) == fault.NotFound {
			return nil, jobs.Permanent(err)
		}
		return nil, err
	}
	inv, err := w.providers.TargetInventory(t)
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	return inv.Inventory(ctx)
}

// inventory: ask a target what it holds, into the cache the page reads.
func (w inventoryRefresh) Run(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	items, err := w.ask(ctx, key)
	if err := w.store(key, items, err); err != nil {
		return nil, err
	}
	return map[string]any{"resources": len(items)}, nil
}

// store writes an ask's outcome into the cache the page reads.
func (w inventoryRefresh) store(key string, items []deploy.Resource, err error) error {
	c := w.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		e = &inventoryEntry{}
		c.entries[key] = e
	}
	e.refreshing = false
	e.asked = clock.Or(c.now)()
	if err != nil {
		e.err = err.Error()
		return err
	}
	e.items, e.err, e.at = items, "", e.asked
	return nil
}
