package forge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/stamp"
)

// The inventory cache (infrastructure_cache.go): the provider is asked
// once, later visits are served from the answer, a demanded refresh asks
// again, and AppTown's own acts drop the cache.
func TestInfrastructureInventoryIsCachedAndDroppedByForgeActs(t *testing.T) {
	t.Parallel()

	a, f, p, b := doWorld(t)
	tg, _ := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	env, _ := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", tg.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	asks := func() int { return f.Snapshot().ListRequests }
	read := func(q string) infrastructureResponse {
		w := tgReq(httpapi.NewInfrastructure(a.Deployment, a.hosts, a.targetProviders, a.Projects, a.DB, a.inventory, a.jobs, a.cfg.usingBuiltinDB, builtinDBContainer, builtinDBVolume, deployment.IntranetTargetName, nil).Infrastructure, "GET", "/api/v1/infrastructure"+q, "", nil)
		var out struct {
			Targets []infrastructureResponse `json:"targets"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		// Every target is on the page now (the built-in host too); this
		// test is about the cloud one.
		for _, v := range out.Targets {
			if v.Target == "do-syd" {
				return v
			}
		}
		t.Fatalf("infrastructure: %d %s", w.Code, w.Body.String())
		return infrastructureResponse{}
	}
	v := read("")
	if !v.Live || !v.AsOf.Valid || asks() != 1 {
		t.Fatalf("first visit asks the provider: live %v asOf %v asks %d", v.Live, v.AsOf, asks())
	}
	read("")
	read("")
	if asks() != 1 {
		t.Fatalf("later visits are served from the answer: asks %d", asks())
	}
	read("?refresh=1")
	if asks() != 2 {
		t.Fatalf("a demanded refresh asks again: asks %d", asks())
	}
	// A release lands: the cache goes, the next visit asks.
	out, _, err := requestBundleForTest(t, a, p, env, b, "bls")
	if err != nil {
		t.Fatal(err)
	}
	release, err := a.Deployment.Releases.ByNumber(context.Background(), p.ID, out["number"].(int64))
	if err != nil {
		t.Fatal(err)
	}
	if release.State != deployment.Released {
		t.Fatalf("release %s", release.State)
	}
	before := asks()
	v = read("")
	if asks() != before+1 || len(v.Resources) < 2 {
		t.Fatalf("after AppTown acts the next visit asks again: asks %d -> %d, %d resources", before, asks(), len(v.Resources))
	}
}

type infrastructureResponse struct {
	Target    string
	Live      bool
	AsOf      stamp.Time
	Resources []deploy.Resource
}

// An AppTown act while an ask is on its way keeps the last answer and its
// time — the page goes on showing what was read and how old it is — and
// marks it so the next visit asks again. Zeroing the time made a live
// attached host read as unreachable with every record missing (sweep 7).
func TestAnActDuringARefreshKeepsTheAnswerAndAsksAgain(t *testing.T) {
	t.Parallel()
	asked := time.Now().Add(-10 * time.Second)
	c := &inventoryCache{entries: map[string]*inventoryEntry{
		"demo-docker": {items: []deploy.Resource{{Kind: "container", Name: "forge-env-app-main"}}, at: asked, asked: asked, refreshing: true},
	}}
	c.Invalidate()
	items, errText, at, refreshing := c.Live(context.Background(), nil, "demo-docker", nil, false)
	if len(items) != 1 || errText != "" || !at.Equal(asked) || !refreshing {
		t.Fatalf("served during the refresh: %v %q %v %v", items, errText, at, refreshing)
	}
	if e := c.entries["demo-docker"]; !e.dropped {
		t.Fatal("the answer that lands is not marked to be asked again")
	}
}

// A target's list ages by AppTown's clock: on a clock ninety days behind
// the wall clock, a list read a moment ago is served as it is, and once
// that clock passes the list's lifetime the next visit asks again.
func TestAnInventoryListAgesByTheForgesClock(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(-90 * 24 * time.Hour)
	var mu sync.Mutex
	clock := at
	c := &inventoryCache{now: func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }, entries: map[string]*inventoryEntry{
		"demo-docker": {items: []deploy.Resource{{Kind: "container", Name: "forge-env-app-main"}}, at: at.Add(-time.Second), asked: at.Add(-time.Second)},
	}}
	if _, _, _, refreshing := c.Live(context.Background(), &jobs.Queue{}, "demo-docker", nil, false); refreshing {
		t.Fatal("a list read a second ago by AppTown's clock was asked for again")
	}
	mu.Lock()
	clock = at.Add(inventoryTTL)
	mu.Unlock()
	if _, _, _, refreshing := c.Live(context.Background(), &jobs.Queue{}, "demo-docker", nil, false); !refreshing {
		t.Fatal("a list past its lifetime by AppTown's clock was not asked for again")
	}
}

// failingInventory answers once with a list, then fails.
type failingInventory struct{ fail bool }

func (f *failingInventory) Inventory(context.Context) ([]deploy.Resource, error) {
	if f.fail {
		return nil, errors.New("ssh: connection refused")
	}
	return []deploy.Resource{{Kind: "container", Name: "forge-env-app-main"}}, nil
}

// A failed ask keeps the last answer under the error with that answer's own
// time, on a demanded refresh and on a queued one; with no answer yet there
// is no time at all. Stamping the failed ask's time on the kept list made an
// hour-old list read as just read.
func TestAFailedAskKeepsTheLastAnswersTime(t *testing.T) {
	t.Parallel()
	read := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	now := read
	c := &inventoryCache{now: func() time.Time { return now }, entries: map[string]*inventoryEntry{}}
	inv := &failingInventory{}
	if _, _, at, _ := c.Live(context.Background(), nil, "demo-docker", inv, true); !at.Equal(read) {
		t.Fatalf("the answer's time: %v", at)
	}
	now, inv.fail = read.Add(time.Hour), true
	items, errText, at, _ := c.Live(context.Background(), nil, "demo-docker", inv, true)
	if len(items) != 1 || errText == "" || !at.Equal(read) {
		t.Fatalf("a demanded refresh that failed: %d items, %q, at %v; want the 01:00 list under the error", len(items), errText, at)
	}
	// The queued refresh (the job) fails the same way.
	now = read.Add(2 * time.Hour)
	w := inventoryRefresh{cache: c}
	c.mu.Lock()
	c.entries["demo-docker"].refreshing = true
	c.mu.Unlock()
	_ = w.store("demo-docker", nil, errors.New("ssh: connection refused"))
	if _, _, at, _ := c.Live(context.Background(), nil, "demo-docker", inv, false); !at.Equal(read) {
		t.Fatalf("a queued refresh that failed stamped the kept list %v, want %v", at, read)
	}
	// A target never answered has no time, so its records stay as recorded.
	if _, errText, at, _ := c.Live(context.Background(), nil, "never-answered", inv, true); errText == "" || !at.IsZero() {
		t.Fatalf("a first ask that failed: %q at %v", errText, at)
	}
}
