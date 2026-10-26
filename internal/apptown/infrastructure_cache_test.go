package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/stamp"
)

// The inventory cache (infrastructure_cache.go): the provider is asked
// once, later visits are served from the answer, a demanded refresh asks
// again, and AppTown's own acts drop the cache.
func TestInfrastructureInventoryIsCachedAndDroppedByAppTownActs(t *testing.T) {
	t.Parallel()

	a, f, p, b := doWorld(t)
	tg, _ := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	env, _ := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", tg.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	asks := func() int { return f.Snapshot().ListRequests }
	read := func(q string) infrastructureResponse {
		w := tgReq(httpapi.NewInfrastructure(a.Deployment, a.hosts, a.targetProviders, a.Projects, a.DB, a.inventory, a.jobs, a.cfg.usingBuiltinDB, currentBuiltinDB.Container, currentBuiltinDB.Volume, nil).Infrastructure, "GET", "/api/v1/infrastructure"+q, "", nil)
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
		"demo-docker": {items: []deploy.Resource{{Kind: "container", Name: "apptown-env-app-main"}}, at: asked, asked: asked, refreshing: true},
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
func TestAnInventoryListAgesByAppTownsClock(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(-90 * 24 * time.Hour)
	var mu sync.Mutex
	clock := at
	c := &inventoryCache{now: func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }, entries: map[string]*inventoryEntry{
		"demo-docker": {items: []deploy.Resource{{Kind: "container", Name: "apptown-env-app-main"}}, at: at.Add(-time.Second), asked: at.Add(-time.Second)},
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
	return []deploy.Resource{{Kind: "container", Name: "apptown-env-app-main"}}, nil
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

type inventoryObservationQueue struct {
	calls int
	err   error
}

func (q *inventoryObservationQueue) RequestInventory(context.Context, string) error {
	q.calls++
	return q.err
}

// A cold snapshot schedules work without a provider; failed refreshes preserve
// old evidence and its age. Caller mutations cannot corrupt the next response.
func TestInventorySnapshotsQueueAndRetainTheirEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	first := now
	c := &inventoryCache{now: func() time.Time { return now }}
	q := &inventoryObservationQueue{}
	for range 3 {
		got, err := c.Snapshot(t.Context(), q, "host")
		if err != nil || !got.Unknown || !got.Stale || len(got.Items) != 0 || q.calls != 1 {
			t.Fatalf("cold coverage: %+v %v calls%d", got, err, q.calls)
		}
	}
	w := inventoryRefresh{cache: c}
	if err := w.store("host", []deploy.Resource{{ID: "one", Tags: []string{"owned"}, Hosts: []deploy.ResourceHost{{Project: "project"}}, Retained: &deploy.RetainedNote{Reason: "retained"}}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := c.Snapshot(t.Context(), q, "host")
	if err != nil || got.Unknown || got.Stale || got.ObservedAt != first {
		t.Fatalf("fresh: %+v %v", got, err)
	}
	got.Items[0].Tags[0] = "changed"
	got.Items[0].Hosts[0].Project = "changed"
	got.Items[0].Retained.Reason = "changed"
	now = now.Add(3 * time.Minute)
	got, err = c.Snapshot(t.Context(), q, "host")
	if err != nil || !got.Stale || got.Items[0].Tags[0] != "owned" || got.Items[0].Hosts[0].Project != "project" || got.Items[0].Retained.Reason != "retained" || q.calls != 2 {
		t.Fatalf("stale immutable: %+v %v", got, err)
	}
	if err := w.store("host", nil, errors.New("provider secret")); err == nil {
		t.Fatal("failure lost")
	}
	got, err = c.Snapshot(t.Context(), q, "host")
	if err != nil || !got.Unknown || got.ObservedAt != first || len(got.Items) != 1 {
		t.Fatalf("failure erased evidence: %+v %v", got, err)
	}
	now = now.Add(time.Minute)
	if err := w.store("host", nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err = c.Snapshot(t.Context(), q, "host")
	if err != nil || got.Unknown || got.Stale || len(got.Items) != 0 || got.ObservedAt != now {
		t.Fatalf("recovery: %+v %v", got, err)
	}
}

func TestInventorySnapshotInvalidationCannotRenewInflightEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := &inventoryCache{now: func() time.Time { return now }}
	q := &inventoryObservationQueue{}
	if _, err := c.Snapshot(t.Context(), q, "host"); err != nil {
		t.Fatal(err)
	}
	c.Invalidate()
	w := inventoryRefresh{cache: c}
	if err := w.store("host", []deploy.Resource{{ID: "before-act"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := c.Snapshot(t.Context(), q, "host")
	if err != nil || !got.Stale {
		t.Fatalf("inflight answer claimed current after an act: %+v %v", got, err)
	}
	now = now.Add(time.Minute)
	got, err = c.Snapshot(t.Context(), q, "host")
	if err != nil || !got.Stale || q.calls != 2 {
		t.Fatalf("replacement scheduling renewed old evidence: %+v %v calls%d", got, err, q.calls)
	}
}

func TestInventoryRefreshSchedulingDoesNotClearInvalidation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := &inventoryCache{now: func() time.Time { return now }, entries: map[string]*inventoryEntry{"host": {at: now, asked: now.Add(-time.Minute), dropped: true, invalidated: true}}}
	q := &inventoryObservationQueue{}
	for range 2 {
		got, err := c.Snapshot(t.Context(), q, "host")
		if err != nil || !got.Stale {
			t.Fatalf("scheduling cleared invalidation: %+v %v", got, err)
		}
	}
	if err := (inventoryRefresh{cache: c}).store("host", nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := c.Snapshot(t.Context(), q, "host")
	if err != nil || got.Stale || got.Unknown {
		t.Fatalf("new post-act evidence not current: %+v %v", got, err)
	}
}

type blockedInventory struct{ started, release chan struct{} }

func (b *blockedInventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	close(b.started)
	select {
	case <-b.release:
		return []deploy.Resource{{ID: "pre-act"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSnapshotCannotOverlapAnActiveDirectInventoryRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := &inventoryCache{now: func() time.Time { return now }, entries: map[string]*inventoryEntry{}}
	inv := &blockedInventory{started: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); c.Live(ctx, nil, "host", inv, false) }()
	<-inv.started
	q := &inventoryObservationQueue{}
	got, err := c.Snapshot(ctx, q, "host")
	if err != nil || !got.Unknown || q.calls != 0 {
		t.Fatalf("snapshot overlapped direct provider read: %+v %v requests%d", got, err, q.calls)
	}
	c.Invalidate()
	close(inv.release)
	<-done
	got, err = c.Snapshot(ctx, q, "host")
	if err != nil || !got.Stale || q.calls != 1 {
		t.Fatalf("pre-act answer became current: %+v %v requests%d", got, err, q.calls)
	}
	if err := (inventoryRefresh{cache: c}).store("host", nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err = c.Snapshot(ctx, q, "host")
	if err != nil || got.Unknown || got.Stale {
		t.Fatalf("post-act observation not current: %+v %v", got, err)
	}
}

func TestInventorySnapshotIsBoundToTargetConfiguration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := &inventoryCache{now: func() time.Time { return now }}
	target := &deployment.Target{Name: "office", SettingsRevision: 1}
	q := &inventoryObservationQueue{}
	first, err := c.SnapshotForTarget(t.Context(), q, target)
	if err != nil || !first.Unknown || q.calls != 1 {
		t.Fatalf("cold target %+v %v", first, err)
	}
	w := inventoryRefresh{cache: c}
	oldIdentity := inventoryTargetIdentity(target)
	if err := w.storeObserved(target.Name, oldIdentity, []deploy.Resource{{ID: "old"}}, nil); err != nil {
		t.Fatal(err)
	}
	warm, err := c.SnapshotForTarget(t.Context(), q, target)
	if err != nil || warm.Unknown || len(warm.Items) != 1 {
		t.Fatalf("matching target %+v %v", warm, err)
	}
	target.SettingsRevision++
	changed, err := c.SnapshotForTarget(t.Context(), q, target)
	if err != nil || !changed.Unknown || len(changed.Items) != 0 || q.calls != 2 {
		t.Fatalf("old address reused %+v %v", changed, err)
	}
	if err := w.storeObserved(target.Name, oldIdentity, []deploy.Resource{{ID: "late"}}, nil); err != nil {
		t.Fatal(err)
	}
	late, err := c.SnapshotForTarget(t.Context(), q, target)
	if err != nil || !late.Unknown || len(late.Items) != 0 {
		t.Fatalf("old in-flight result published %+v %v", late, err)
	}
	if err := w.storeObserved(target.Name, inventoryTargetIdentity(target), []deploy.Resource{{ID: "current"}}, nil); err != nil {
		t.Fatal(err)
	}
	current, err := c.SnapshotForTarget(t.Context(), q, target)
	if err != nil || current.Unknown || len(current.Items) != 1 || current.Items[0].ID != "current" {
		t.Fatalf("current target %+v %v", current, err)
	}
}

type inventoryTargetOnly struct{ target *deployment.Target }

func (s inventoryTargetOnly) All(context.Context) ([]*deployment.Target, error) {
	return []*deployment.Target{s.target}, nil
}

func TestAttentionInventoryObservesActualProviderLossFailureAndRecovery(t *testing.T) {
	t.Parallel()
	a, f, p, b := doWorld(t)
	ctx := t.Context()
	target, err := a.targets.ByName(ctx, "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.deployments.EnsureStanding(ctx, p.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := requestBundleForTest(t, a, p, dep, b, "bls"); err != nil {
		t.Fatal(err)
	}
	current, err := a.deployments.ByID(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	appID, ok := current.Handle["appId"].(string)
	if !ok || appID == "" {
		t.Fatal("fixture did not release an app")
	}
	cfg, err := a.targetProviders.DigitalOceanConfig(ctx, target.Provider)
	if err != nil {
		t.Fatal(err)
	}
	client, err := a.targetProviders.DigitalOceanClient(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	source := &attention.Inventory{Targets: inventoryTargetOnly{target}, Records: a.Deployment.Inventory, Observer: attentionInventory{cache: a.inventory, requests: inventoryRequests{db: a.DB}}}
	read := func() attention.Snapshot {
		t.Helper()
		before := f.Snapshot().ListRequests
		got, err := source.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f.Snapshot().ListRequests != before {
			t.Fatal("Attention performed inline provider IO")
		}
		return got
	}
	missing := func(s attention.Snapshot) []attention.Item {
		var out []attention.Item
		for _, i := range s.Items {
			if i.Title == "A recorded resource is missing" {
				out = append(out, i)
			}
		}
		return out
	}
	job := inventoryRefresh{targets: a.targets, providers: a.targetProviders, cache: a.inventory}
	if cold := read(); cold.Known || len(missing(cold)) != 0 {
		t.Fatalf("cold coverage %+v", cold)
	}
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}
	if warm := read(); !warm.Known || len(missing(warm)) != 0 {
		t.Fatalf("released provider resource missing %+v", warm)
	}
	// Change only the disposable provider account, keeping the deployed record.
	if err := client.Call(ctx, "DELETE", "/v2/apps/"+appID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}
	absent := read()
	conditions := missing(absent)
	if !absent.Known || len(conditions) != 1 {
		t.Fatalf("complete provider absence %+v", absent)
	}
	key, at := conditions[0].Key, conditions[0].ObservedAt
	f.Fail("GET", "/v2/apps", 503)
	if _, err := job.Run(ctx, target.Name, nil); err == nil {
		t.Fatal("provider failure ignored")
	}
	failed := read()
	conditions = missing(failed)
	if failed.Known || len(conditions) != 1 || conditions[0].Key != key || conditions[0].ObservedAt != at || !conditions[0].Stale {
		t.Fatalf("failed refresh erased evidence %+v", failed)
	}
	f.Fail("GET", "/v2/apps", 0)
	f.SeedApp(appID, "recovered", "")
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}
	if recovered := read(); !recovered.Known || len(missing(recovered)) != 0 {
		t.Fatalf("provider recovery not observed %+v", recovered)
	}
}
