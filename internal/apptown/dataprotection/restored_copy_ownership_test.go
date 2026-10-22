package dataprotection

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A restored copy's take-down deletes only what is provably its own (round
// 2 of the build review, RTB2-01). Project "ledger-pay"'s copy-1 and
// project "ledger"'s standing "pay-copy-1" once joined into the same App
// Platform app and cluster names. Names are now made from each
// deployment's key, so the copy is named apart from the other project's
// deployment, and its start and take-down never touch the other's app or
// cluster.
func TestATakeDownNeverDeletesAnotherProjectsAppOfTheSameName(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	// A project's slug is fixed when it is made, and this world makes
	// "ledger"; the collision needs the hyphenated slug, which no operation
	// gives an existing project any more.
	// sql-guarantee: the world's project takes the colliding slug directly.
	if _, err := w.db.Exec(`UPDATE projects SET slug='ledger-pay' WHERE id=$1`, w.project); err != nil {
		t.Fatal(err)
	}
	other := createTestProject(t, w.db, "ledger", "Other Ledger")
	otherDep, err := w.bind(other, "pay-copy-1", "synthetic", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	key := deploy.HostKey(otherDep.ID)
	client, err := w.cloudClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var made struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	if err := client.Call(ctx, "POST", "/v2/apps", map[string]any{"spec": map[string]any{"name": "apptown-" + key}}, &made); err != nil {
		t.Fatal(err)
	}
	before := len(w.api.Snapshot().Requests)
	w.api.SeedDatabase(map[string]any{"id": "other-cluster", "name": "apptown-db-" + key, "engine": "pg", "version": "16", "status": "online", "tags": []string{"apptown", "apptown-project-ledger", "apptown-deployment-pay-copy-1"}})
	w.start(RestoredCopyRequest{Test: true})
	for _, r := range w.api.Snapshot().Requests[before:] {
		if (r.Method == "PUT") && r.Path == "/v2/apps/"+made.App.ID {
			t.Fatalf("the copy changed the other project's app: %+v", r)
		}
		if r.Method == "DELETE" && (r.Path == "/v2/apps/"+made.App.ID || r.Path == "/v2/databases/other-cluster") {
			t.Fatalf("the copy deleted the other project's %s", r.Path)
		}
	}
	found := false
	for _, app := range w.api.Snapshot().Apps {
		found = found || app["id"] == made.App.ID
	}
	if !found {
		t.Fatal("the other project's app was deleted")
	}
}

// Cluster lookup takes a cluster only with both of the copy's tags, and app
// lookup never takes an app without the copy's mark, whatever its name
// (reviewer's boundary check, kept).
func TestFindMadeTakesOnlyMarkedClustersAndApps(t *testing.T) {
	t.Parallel()
	id := idstest.N(4242)
	key := deploy.HostKey(id)
	for _, tags := range [][]string{nil, {"apptown-project-ledger"}, {"apptown-deployment-copy-1"}, {"apptown-project-other", "apptown-deployment-copy-1"}, {"apptown-project-ledger", "apptown-deployment-other"}, {"apptown-project-ledger", "apptown-deployment-copy-1"}} {
		t.Run(fmt.Sprint(tags), func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, nil)
			ctx := context.Background()
			w.api.SeedDatabase(map[string]any{"id": "candidate", "name": "apptown-db-" + key, "tags": tags})
			client, err := w.cloudClient(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// The app named by the copy's key, and names that only begin or
			// end like it.
			for _, name := range []string{"apptown-" + key, "x-apptown-" + key, "apptown-" + key + "0", "apptown-" + key + "-extra"} {
				if err := client.Call(ctx, "POST", "/v2/apps", map[string]any{"spec": map[string]any{"name": name}}, nil); err != nil {
					t.Fatal(err)
				}
			}
			driver, err := w.s.providers.Driver(ctx, w.target, w.prod)
			if err != nil {
				t.Fatal(err)
			}
			made, err := driver.(*digitalocean.Driver).FindMade(ctx, deploy.Placement{ID: id, Name: "copy-1", Gate: &deploy.Gate{Marker: strings.Repeat("c", 64)}})
			if err != nil {
				t.Fatal(err)
			}
			expected := len(tags) == 2 && tags[0] == "apptown-project-ledger" && tags[1] == "apptown-deployment-copy-1"
			if (len(made.Clusters) == 1) != expected || made.AppID != "" || len(made.NotOurs) != 1 {
				t.Fatalf("lookup = %+v", made)
			}
		})
	}
}

// A database lookup that fails keeps the copy standing; once it answers,
// the sweep takes the copy down (reviewer's boundary check, kept).
func TestADatabaseLookupFailureKeepsTheCopyUntilTheSweepSucceeds(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	_, c := w.start(RestoredCopyRequest{Minutes: 30})
	w.api.Fail("GET", "/v2/databases", 500)
	op, err := w.s.TakeDownRestoredCopy(ctx, w.project, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); got.State != operations.Failed || !strings.Contains(got.Detail, "whether anything stands is unknown") {
		t.Fatalf("operation = %+v", got)
	}
	copies, err := w.s.RestoredCopiesOf(ctx, w.prod, 5)
	if err != nil || copies[0].EndedAt.Valid || len(w.api.Snapshot().Apps) != 2 {
		t.Fatalf("a lookup failure ended the copy: %v %v", copies, err)
	}
	w.api.Fail("GET", "/v2/databases", 0)
	n, _, err := w.s.sweepRestoredCopies(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	ops, err := w.opsOn(c.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(&ops[0]); !endedAs(got, OperationDone) {
		t.Fatalf("retry = %+v", got)
	}
	if state := w.api.Snapshot(); len(state.Apps) != 1 || len(state.Databases) != 1 {
		t.Fatalf("after the retry: apps=%d databases=%d", len(state.Apps), len(state.Databases))
	}
}
