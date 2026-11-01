package dataprotection

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/ids"
)

// The effects each deployment kind asks the admitting credential about
// again, the stops a take-down honours, and the ends the ledger must not
// report as success: the table in internal/apptown/operations/doc.go, held
// to what the kinds do. Each test withdraws authority, asks a stop, or
// loses an answer while an earlier step is under way, then lets it go on.

// releasedProd is the ledger's production deployment, released, holding
// records in its cluster, with the backup store attached.
func releasedProd(t *testing.T) (*dataWorld, *deployment.Deployment, string) {
	t.Helper()
	w := newDataWorld(t)
	w.protect()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	cluster, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(cluster+".fake", "app", "ledger: 41, 42")
	return w, dep, cluster
}

func withdrawn(t *testing.T, op *operations.Operation) {
	t.Helper()
	if op.State != operations.Failed || !strings.Contains(op.Detail, "authority withdrawn") {
		t.Fatalf("an operation whose authority was withdrawn: %s %q", op.State, op.Detail)
	}
}

// A copy asks again after its export, before it uploads: a token revoked
// while the export runs uploads nothing.
//
// Kills: checking the copy's authority only when it starts.
func TestACopyAsksAgainBeforeItUploads(t *testing.T) {
	t.Parallel()
	w, _, _ := releasedProd(t)
	op, err := w.s.RequestCopy(context.Background(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	w.clusters.atExport = func() { w.auth.revoke("token:alice") }
	withdrawn(t, w.run(op))
	for _, key := range w.store.Keys("forge-copies") {
		if strings.HasSuffix(key, ".dump") {
			t.Fatalf("a copy was uploaded after its authority was withdrawn: %s", key)
		}
	}
}

// A drill asks again after making its new database, before it fills it
// with production data, and removes the empty database it made.
//
// Kills: a drill that fills a new database under withdrawn authority.
func TestADrillAsksAgainBeforeItFills(t *testing.T) {
	t.Parallel()
	w, _, cluster := releasedProd(t)
	ctx := context.Background()
	copied, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(copied); !endedAs(got, operationNotVerified) {
		t.Fatalf("the copy: %s %s", got.State, got.Detail)
	}
	op, err := w.s.RequestDrill(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	filled := false
	w.clusters.atFill = func() { filled = true }
	w.mu.Lock()
	w.onRequest = func(req string) {
		if req == "POST /v2/databases/"+cluster+"/dbs" {
			w.auth.revoke("token:alice")
		}
	}
	w.mu.Unlock()
	withdrawn(t, w.run(op))
	if filled {
		t.Fatal("the drill filled its database after its authority was withdrawn")
	}
	if _, ok := w.clusters.get(cluster+".fake", checkPlace("app", op.ID)); ok {
		t.Fatal("the drill left the database it made")
	}
}

// A take-down asks again before each phase after deleting the app: one
// whose authority is withdrawn once the app is gone takes no final copy and
// deletes no cluster; one withdrawn while its final copy is checked deletes
// no cluster.
//
// Kills: checking a take-down's authority only before the app goes.
func TestATakeDownAsksAgainBeforeEachLaterEffect(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"after the app is deleted", "while its copy is checked"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			w, _, _ := releasedProd(t)
			res, err := w.s.TakeDown(context.Background(), RemoveDeployment{ProjectID: w.project, Name: "prod"})
			if err != nil || res.Operation == nil {
				t.Fatalf("take-down: %+v %v", res, err)
			}
			revoke := func() { w.auth.revoke("token:alice") }
			if when == "after the app is deleted" {
				w.s.applied = func(_ ids.ID) { revoke() }
			} else {
				w.clusters.atFill = revoke
			}
			withdrawn(t, w.run(res.Operation))
			if deleted := w.api.Snapshot().DeletedClusters; len(deleted) != 0 {
				t.Fatalf("clusters deleted after authority was withdrawn: %v", deleted)
			}
			sets, err := w.s.backups.ForDeployment(context.Background(), w.project, "prod", 10)
			if err != nil {
				t.Fatal(err)
			}
			if when == "after the app is deleted" && len(sets) != 0 {
				t.Fatalf("a final copy was taken after authority was withdrawn: %+v", sets)
			}
			if _, err := w.s.Deployments.ByName(t.Context(), w.project, "prod"); err != nil {
				t.Fatalf("the deployment's record went: %v", err)
			}
		})
	}
}

// A take-down asked to stop while its final copy is checked stops before
// deleting the cluster, and reads cancelled: the app is gone and the data
// kept, as it says.
//
// Kills: a cancel request a take-down never reads; a stop read as failed.
func TestATakeDownStopsAsAskedBeforeDeletingTheCluster(t *testing.T) {
	t.Parallel()
	w, _, _ := releasedProd(t)
	ctx := context.Background()
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down: %+v %v", res, err)
	}
	w.clusters.atFill = func() {
		if _, err := w.ops.Cancel(ctx, res.Operation.ID); err != nil {
			t.Errorf("cancelling the running take-down: %v", err)
		}
	}
	got := w.run(res.Operation)
	if got.State != operations.Cancelled || !strings.Contains(got.Detail, "app removed, data kept") || !strings.Contains(got.Detail, "stopped as asked before deleting the cluster") {
		t.Fatalf("a take-down asked to stop: %s %q", got.State, got.Detail)
	}
	if deleted := w.api.Snapshot().DeletedClusters; len(deleted) != 0 {
		t.Fatalf("clusters deleted after a stop was asked: %v", deleted)
	}
}

// Deleting the app gets no answer (the provider deleted it and the
// response was lost): the take-down reads unknown, its step not known, and
// does not claim the app stands.
//
// Kills: an indeterminate provider effect reported as a known failure.
func TestATakeDownWhoseDeleteGotNoAnswerIsUnknown(t *testing.T) {
	t.Parallel()
	w, _, _ := releasedProd(t)
	res, err := w.s.TakeDown(context.Background(), RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down: %+v %v", res, err)
	}
	apps := w.api.Snapshot().Apps
	if len(apps) != 1 {
		t.Fatalf("apps: %v", apps)
	}
	w.api.LoseAnswer("DELETE", "/v2/apps/"+fmt.Sprint(apps[0]["id"]))
	got := w.run(res.Operation)
	last := got.Steps[len(got.Steps)-1]
	if got.State != operations.Unknown || !last.Unknown || !strings.Contains(got.Detail, "not known") || strings.Contains(got.Detail, "could not be deleted") {
		t.Fatalf("a delete that got no answer: %s %q, last step %+v", got.State, got.Detail, last)
	}
	if len(w.api.Snapshot().Apps) != 0 {
		t.Fatal("the fake did not apply the delete it lost the answer to")
	}
}

// A drill whose data came back but whose database could not be removed
// does not succeed: the copy coming back stays recorded (its verification
// and its drill), the operation fails naming what it left, and reads
// unknown when whether it was removed is not known.
//
// Kills: a drill that leaves production data behind and succeeds.
func TestADrillThatLeavesItsDatabaseDoesNotSucceed(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"refused", "lost"} {
		t.Run(answer, func(t *testing.T) {
			t.Parallel()
			w, _, cluster := releasedProd(t)
			ctx := context.Background()
			copied, err := w.s.RequestCopy(ctx, w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			w.run(copied)
			op, err := w.s.RequestDrill(ctx, w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			path := "/v2/databases/" + cluster + "/dbs/" + checkPlace("app", op.ID)
			want := operations.Failed
			if answer == "refused" {
				w.api.Fail("DELETE", path, 422)
			} else {
				w.api.LoseAnswer("DELETE", path)
				want = operations.Unknown
			}
			got := w.run(op)
			if got.State != want || got.Verification != operations.Verified || !strings.Contains(got.Detail, "for a person to remove") {
				t.Fatalf("a drill that left its database: %s/%s %q", got.State, got.Verification, got.Detail)
			}
			p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
			if err != nil || p.Items[0].Drill == nil || !p.Items[0].Drill.Passed {
				t.Fatalf("the drill's evidence: %+v %v", p, err)
			}
		})
	}
}

// A rehearsal asks again before it starts the app on the copy: a token
// revoked while the copy is being filled starts no app.
//
// Kills: a rehearsal that runs the app under withdrawn authority.
func TestARehearsalAsksAgainBeforeItStartsTheApp(t *testing.T) {
	t.Parallel()
	w := newRehearsalWorld(t)
	op, err := w.s.RequestRehearsal(context.Background(), w.project, "prod", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	w.host.mu.Lock()
	w.host.beforeImport = func() { w.auth.revoke("token:alice") }
	w.host.mu.Unlock()
	withdrawn(t, w.run(op))
	name := fmt.Sprintf("apptown-rehearsal-%d", op.ID)
	for _, c := range w.host.Commands() {
		if c[0] == "run" && slices.Contains(c, "--name") && c[slices.Index(c, "--name")+1] == name {
			t.Fatalf("the app was started after authority was withdrawn: %v", c)
		}
	}
}

// A restored copy's start asks again before it fills its new database
// and before it deploys: a token revoked while the copy's cluster is being
// made fills nothing and deploys nothing, and what was made is taken down.
//
// Kills: a restore that goes on provisioning, filling and deploying after
// its authority was withdrawn.
func TestARestoredCopyAsksAgainBeforeItFillsAndDeploys(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	apps := len(w.api.Snapshot().Apps)
	filled := false
	w.clusters.atFill = func() { filled = true }
	w.mu.Lock()
	w.onRequest = func(req string) {
		if req == "POST /v2/databases" {
			w.auth.revoke("token:alice")
		}
	}
	w.mu.Unlock()
	op, c := w.start(RestoredCopyRequest{})
	withdrawn(t, op)
	if filled || len(w.api.Snapshot().Apps) != apps || !c.EndedAt.Valid {
		t.Fatalf("after authority was withdrawn: filled %v, apps %d (was %d), copy %+v", filled, len(w.api.Snapshot().Apps), apps, c)
	}
}

// A restored copy's take-down asks again before each removal: one whose
// authority is withdrawn once its app is removed deletes no cluster, and the
// copy stays standing for the sweep.
//
// Kills: a take-down loop that removes everything under the first check.
func TestARestoredCopysTakeDownAsksBeforeEachRemoval(t *testing.T) {
	t.Parallel()
	w := newRestoredWorld(t, nil)
	ctx := context.Background()
	c := w.standing()
	op, err := w.s.TakeDownRestoredCopy(ctx, w.project, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.s.applied = func(_ ids.ID) { w.auth.revoke("token:alice") }
	withdrawn(t, w.run(op))
	if deleted := w.api.Snapshot().DeletedClusters; len(deleted) != 0 {
		t.Fatalf("clusters deleted after authority was withdrawn: %v", deleted)
	}
	copies, err := w.s.restoredCopiesOf(ctx, w.prod, 5)
	if err != nil || len(copies) == 0 || copies[0].EndedAt.Valid {
		t.Fatalf("the copy after a refused take-down: %+v %v", copies, err)
	}
}

// A restored copy's fill asks again after reading each copy back, before
// making its database new, and again before filling it: a token revoked
// while the copy is read back makes nothing new and fills nothing, and one
// revoked once the database is made fills nothing.
//
// Kills: a fill that drops, makes and imports under the one check before it.
func TestARestoredCopysFillAsksBeforeEachEffect(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"while the copy is read back", "once the database is made"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, nil)
			filled, made, revoked, posts := false, false, false, 0
			w.clusters.atFill = func() { filled = true }
			revoke := func() { revoked = true; w.auth.revoke("token:alice") }
			if when == "while the copy is read back" {
				w.store.SetBefore(func(c s3fake.Call) {
					if c.Method == "GET" && strings.HasSuffix(c.Key, ".dump") {
						revoke()
					}
				})
			}
			w.mu.Lock()
			w.onRequest = func(req string) {
				if strings.HasPrefix(req, "POST /v2/databases/") && strings.HasSuffix(req, "/dbs") && !strings.Contains(req, w.source) {
					// Made after the revocation; a need's database made with
					// its cluster, before the fill, is not one.
					made = made || revoked
					posts++
					// The second is the fill's own database, made new.
					if when == "once the database is made" && posts == 2 {
						revoke()
						made = false
					}
				}
			}
			w.mu.Unlock()
			op, _ := w.start(RestoredCopyRequest{})
			withdrawn(t, op)
			if !revoked || filled || made {
				t.Fatalf("after authority was withdrawn %s: database made %v, filled %v", when, made, filled)
			}
		})
	}
}

// A drill whose new database got no answer to its making names it as
// possibly made and reads unknown: the creation step is not known, and it
// is neither forgotten nor removed on a guess.
//
// Kills: an uncertain creation reported as "no new place".
func TestADrillWhoseDatabaseMayHaveBeenMadeIsUnknown(t *testing.T) {
	t.Parallel()
	w, _, cluster := releasedProd(t)
	ctx := context.Background()
	copied, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	w.run(copied)
	op, err := w.s.RequestDrill(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	w.api.LoseAnswer("POST", "/v2/databases/"+cluster+"/dbs")
	got := w.run(op)
	var creation *operations.OperationStep
	for i := range got.Steps {
		if strings.HasPrefix(got.Steps[i].Name, "filling a new database") {
			creation = &got.Steps[i]
		}
	}
	place := checkPlace("app", op.ID)
	if got.State != operations.Unknown || creation == nil || !creation.Unknown || !strings.Contains(got.Detail, place) || strings.Contains(got.Detail, "no new place") {
		t.Fatalf("a drill whose database may have been made: %s %q steps %+v", got.State, got.Detail, got.Steps)
	}
	if _, ok := w.clusters.get(cluster+".fake", place); !ok {
		t.Fatal("the fake did not make the database whose answer it lost")
	}
}

// Queued copy-start releases retain restore authority over both projects.
// A denial before execution makes no provider resources; a denial during
// provisioning prevents filling or deploying, while preserving failed-release
// attribution. The current-authority control proves the same copy can start.
func TestQueuedCopyStartRechecksBothProjectsBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"current", "source before execution", "destination before execution", "source after cluster", "destination after cluster"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			w, _, _ := releasedProd(t)
			ctx := t.Context()
			op, err := w.s.RequestCopy(ctx, w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			copied := w.run(op)
			raw, _ := copied.Result["backup"].(string)
			set, err := ids.Parse(raw)
			if err != nil || set.IsZero() {
				t.Fatalf("copy: %+v", copied)
			}
			destination := createTestProject(t, w.db, "copy-destination", "Copy destination")
			dep, err := w.bind(destination, "copy", "production", set)
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := w.s.Bundles.Cut(ctx, destination, w.bundle.ContractRevision, map[string]string{"web": "sha256:web"}, nil, w.contract, deployment.BundleOrigin{})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: destination, DeploymentName: dep.Name, BundleID: bundle, AcceptUncheckedCopy: true})
			if err != nil {
				t.Fatal(err)
			}
			before := w.api.Snapshot()
			filled := false
			w.clusters.atFill = func() { filled = true }
			revoke := func() {
				project := w.project
				if strings.HasPrefix(when, "destination") {
					project = destination
				}
				w.auth.set("alice", "deployment:restore@"+project.String())
			}
			if strings.HasSuffix(when, "before execution") {
				revoke()
			}
			w.mu.Lock()
			w.onRequest = func(req string) {
				if strings.HasSuffix(when, "after cluster") && req == "POST /v2/databases" {
					revoke()
				}
			}
			w.mu.Unlock()
			_ = w.s.ExecuteRelease(ctx, accepted.ID)
			release, err := w.s.Releases.ByNumber(ctx, destination, accepted.Number)
			if err != nil {
				t.Fatal(err)
			}
			after := w.api.Snapshot()
			if when == "current" {
				if release.State != deployment.Released || !filled || len(after.Apps) != len(before.Apps)+1 {
					t.Fatalf("current authority failed: release=%+v filled=%v", release, filled)
				}
			} else {
				if release.State != deployment.ReleaseFailed || filled || len(after.Apps) != len(before.Apps) {
					t.Fatalf("denied restore continued: release=%+v filled=%v", release, filled)
				}
				if strings.HasSuffix(when, "before execution") && len(after.Databases) != len(before.Databases) {
					t.Fatal("made a cluster after restore authority was withdrawn")
				}
			}
		})
	}
}
