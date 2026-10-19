package dataprotection

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

// A restored copy's take-down deletes only what is provably its own (round
// 2 of the build review, RTB2-01). Project "ledger-pay"'s copy-1 and
// project "ledger"'s standing "pay-copy-1" join into the same App Platform
// app name, ledger-pay-copy-1. The copy's start is refused at the other
// deployment's cluster (its tags are not the copy's) before any app is
// made, and its take-down finds the other project's app by that name: it
// does not carry the copy's mark, so it is left standing and said, never
// deleted.
func TestATakeDownNeverDeletesAnotherProjectsAppOfTheSameName(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	if err := project.New(w.db, nil).Rename(ctx, w.project, "ledger-pay", "Ledger Pay"); err != nil {
		t.Fatal(err)
	}
	other := createTestProject(t, w.db, "ledger", "Other Ledger")
	if _, err := w.bind(other, "pay-copy-1", "synthetic", ids.Nil); err != nil {
		t.Fatal(err)
	}
	client, err := w.cloudClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var made struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	if err := client.Call(ctx, "POST", "/v2/apps", map[string]any{"spec": map[string]any{"name": "ledger-pay-copy-1"}}, &made); err != nil {
		t.Fatal(err)
	}
	before := len(w.api.Snapshot().Requests)
	w.api.SeedDatabase(map[string]any{"id": "other-cluster", "name": "forge-ledger-pay-copy-1", "engine": "pg", "version": "16", "status": "online", "tags": []string{"forge-project-ledger", "forge-env-pay-copy-1"}})
	op, c := w.start(RestoredCopyRequest{Test: true})
	if op.State != operations.Failed || !strings.Contains(op.Detail, "does not carry this AppTown deployment") {
		t.Fatalf("start = %s: %s", op.State, op.Detail)
	}
	for _, r := range w.api.Snapshot().Requests[before:] {
		if (r.Method == "POST" || r.Method == "PUT") && strings.HasPrefix(r.Path, "/v2/apps") {
			t.Fatalf("the copy made an app: %+v", r)
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
	if !strings.Contains(c.EndDetail, "found and left alone, as not this copy's: an app named ledger-pay-copy-1") {
		t.Fatalf("the copy's end does not say what it left alone: %+v", c)
	}
}

// Cluster lookup takes a cluster only with both of the copy's tags, and app
// lookup never takes an app without the copy's mark, whatever its name
// (reviewer's boundary check, kept).
func TestFindMadeTakesOnlyMarkedClustersAndApps(t *testing.T) {
	t.Parallel()
	for _, tags := range [][]string{nil, {"forge-project-ledger"}, {"forge-env-copy-1"}, {"forge-project-other", "forge-env-copy-1"}, {"forge-project-ledger", "forge-env-other"}, {"forge-project-ledger", "forge-env-copy-1"}} {
		t.Run(fmt.Sprint(tags), func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, nil)
			ctx := context.Background()
			w.api.SeedDatabase(map[string]any{"id": "candidate", "name": "forge-ledger-copy-1", "tags": tags})
			client, err := w.cloudClient(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"ledger-copy-1", "other-ledger-copy-1", "ledger-copy-10", "ledger-copy-1-extra"} {
				if err := client.Call(ctx, "POST", "/v2/apps", map[string]any{"spec": map[string]any{"name": name}}, nil); err != nil {
					t.Fatal(err)
				}
			}
			driver, err := w.s.providers.Driver(ctx, w.target, w.prod)
			if err != nil {
				t.Fatal(err)
			}
			made, err := driver.(*digitalocean.Driver).FindMade(ctx, deploy.Placement{Name: "copy-1", Gate: &deploy.Gate{Marker: strings.Repeat("c", 64)}})
			if err != nil {
				t.Fatal(err)
			}
			expected := len(tags) == 2 && tags[0] == "forge-project-ledger" && tags[1] == "forge-env-copy-1"
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
