package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// Deleting a project takes its production App Platform deployment down the
// way any such deployment is taken down when a backup store is attached:
// the app goes, a final copy goes to the store and comes back into a new
// database, and only then is the cluster deleted. The deletion runs that
// operation itself and waits for it, as its only runner: while its copy is
// being checked, the queue dispatches every operation job waiting, as it
// does in production, and none may be there for it. main goes too, and any
// other deployment the same way.
//
// Kills: project deletion calling Remove directly (no final copy, the
// cluster kept as a retained row); the operation refusing main because the
// deletion's withProject was not passed to it; a failed operation read as
// a deletion that may go on; a job queued for the operation the deletion
// runs itself, so the queue runs it a second time.
func TestProjectDeletionTakesAProtectedDeploymentDownWithAVerifiedFinalCopy(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"main", "prod"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			takeDownWithProject(t, name)
		})
	}
}

func takeDownWithProject(t *testing.T, name string) {
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	dep, err := w.bind(w.project, name, "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release(name); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	clusterID, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(clusterID+".fake", "app", "ledger: 41, 42")
	// The queue, mid-operation: each waiting operation job is delivered to
	// the operations worker.
	dispatched, once := 0, false
	w.clusters.atFill = func() {
		if once {
			return
		}
		once = true
		var keys []string
		if err := w.db.InTx(ctx, func(tx *database.Tx) error {
			var err error
			keys, err = jobs.UnfinishedKeysIn(ctx, tx.Native(), operations.Job)
			return err
		}); err != nil {
			t.Error(err)
			return
		}
		for _, k := range keys {
			receipt, err := w.ops.JobHandler(ctx, k, nil)
			if err != nil || receipt["state"] != "running" {
				t.Errorf("a second delivery of operation %s: %v %v", k, receipt, err)
			}
			dispatched++
		}
	}

	res, err := w.s.TakeDownWithProject(ctx, w.project, name)
	if err != nil {
		t.Fatal(err)
	}
	if !once || dispatched != 1 {
		t.Fatalf("deliveries of the operation's job while it ran: %d (dispatch reached: %v)", dispatched, once)
	}
	if res.FinalBackup == nil {
		t.Fatalf("project deletion took no final copy: %+v", res)
	}
	ops, err := w.ops.OnSubject(ctx, deployment.SubjectOf(dep), 10)
	if err != nil || len(ops) != 1 || ops[0].Kind != OperationTakeDown || !endedAs(&ops[0], OperationVerified) {
		t.Fatalf("the take-down operation: %+v %v", ops, err)
	}
	var names []string
	for _, st := range ops[0].Steps {
		names = append(names, st.Name)
	}
	order := strings.Join(names, " | ")
	copied, checked, cluster := strings.Index(order, "copying postgres"), strings.Index(order, "filling a new database"), strings.Index(order, "deleting the cluster")
	if copied < 0 || checked < copied || cluster < checked {
		t.Fatalf("the cluster was not deleted after its final copy came back: %s", order)
	}
	state := w.api.Snapshot()
	if len(state.Apps) != 0 || len(state.DeletedClusters) != 1 || state.DeletedClusters[0] != clusterID {
		t.Fatalf("after the take-down: apps %d, deleted clusters %v", len(state.Apps), state.DeletedClusters)
	}
	finals, err := w.s.FinalCopies(ctx, w.project)
	if err != nil || len(finals) != 1 || !finals[0].Verified || finals[0].Backup != res.FinalBackup.ID || finals[0].Deployment != name {
		t.Fatalf("final copies: %+v %v (result %+v)", finals, err, res.FinalBackup)
	}
	set, err := w.s.backups.ByID(ctx, w.project, name, res.FinalBackup.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := set.GoodCopy("need", "postgres")
	if set.Store != StoreBackupStore || item == nil || item.Instance != clusterID {
		t.Fatalf("the final set: %+v", set)
	}
	if obj, ok := w.store.Get("forge-copies", set.Path+"/"+item.File); !ok || string(obj.Body) != "PGCOPY\nledger: 41, 42" {
		t.Fatalf("the copy in the store: %+v %v", obj, ok)
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, name); err == nil {
		t.Fatalf("%s's record stands after its take-down", name)
	}
	if open, err := w.s.Retained.ForProject(ctx, w.project); err != nil || len(open) != 0 {
		t.Fatalf("a checked take-down left %v (%v)", open, err)
	}
}

// A take-down operation that ends short refuses the project's deletion with
// what it left: the data stays where it is, with the deployment's record.
func TestProjectDeletionIsRefusedWhenTheFinalCopyFallsShort(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "main", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("main"); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	w.clusters.failExport = true
	_, err := w.s.TakeDownWithProject(ctx, w.project, "main")
	if err == nil || fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), "app removed, data kept") {
		t.Fatalf("a take-down whose final copy failed: %v (%v)", err, fault.KindOf(err))
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, "main"); err != nil {
		t.Fatalf("main's record went with its data unprotected: %v", err)
	}
	if deleted := w.api.Snapshot().DeletedClusters; len(deleted) != 0 {
		t.Fatalf("clusters deleted with no final copy: %v", deleted)
	}
}

// An eval copy's deployments go with no final copy, even on a target where
// any other deployment's take-down takes one: the copy's data is synthetic
// and nothing will restore it.
//
// Kills: the take-down operation (and its final copy) started for an eval
// copy.
func TestAnEvalCopysDeploymentGoesWithNoFinalCopy(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	dep, err := w.bind(w.project, "main", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("main"); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	// Marked a copy after its release: a copy is never placed on App
	// Platform, but nothing about its take-down may rest on that.
	if p, err := w.s.DeploymentProtection(ctx, w.project, "main"); err != nil || p.TakeDown == "" {
		t.Fatalf("an ordinary deployment's take-down says nothing of its data: %+v %v", p, err)
	}
	markCopy(t, w.db, w.project)
	// Its protection page says nothing of a final copy either.
	if p, err := w.s.DeploymentProtection(ctx, w.project, "main"); err != nil || p.TakeDown != "" {
		t.Fatalf("a copy's take-down words: %+v %v", p, err)
	}
	res, err := w.s.TakeDownWithProject(ctx, w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalBackup != nil {
		t.Fatalf("a final copy of synthetic data: %+v", res.FinalBackup)
	}
	if ops, err := w.ops.OnSubject(ctx, deployment.SubjectOf(dep), 10); err != nil || len(ops) != 0 {
		t.Fatalf("an operation for an eval copy's take-down: %+v %v", ops, err)
	}
	if sets, err := w.s.backups.ForDeployment(ctx, w.project, "main", 10); err != nil || len(sets) != 0 {
		t.Fatalf("sets of an eval copy's data: %+v %v", sets, err)
	}
}

// The take-down door does not run its operation: it admits it with the
// job that does, and answers with the operation, queued.
//
// Kills: the door recording its operation without a job, so nothing runs it.
func TestTheTakeDownDoorQueuesItsOperation(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down door: %+v %v", res, err)
	}
	var keys []string
	if err := w.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		keys, err = jobs.UnfinishedKeysIn(ctx, tx.Native(), operations.Job)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res.Operation.State != operations.Queued {
		t.Fatalf("the door's operation reads %s", res.Operation.State)
	}
	if len(keys) != 1 || keys[0] != fmt.Sprint(res.Operation.ID) {
		t.Fatalf("operation jobs waiting: %v, want operation %d's", keys, res.Operation.ID)
	}
}

// A take-down that could take no final copy of the deployment's data (here
// its target lost its copy runner) is refused, with the same sentence its
// data operations are refused with, at the take-down door and at project
// deletion: nothing is deleted and its record stays. It goes only when the
// person chooses to take it down without a final copy, naming it; then the
// app goes, the cluster stays as a retained resource, and the receipt
// records the choice and why there was no copy. The card says so first.
//
// Kills: falling back to the plain removal unasked; project deletion going
// on without the choice; a choice that names another deployment; a receipt
// that does not say the take-down went with no final copy; the card
// promising a final copy it cannot take.
func TestATakeDownThatCannotTakeAFinalCopyIsRefusedUnlessChosen(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	if p, err := w.s.DeploymentProtection(ctx, w.project, "prod"); err != nil || !p.Operations.FinalCopy.Available || !strings.Contains(p.TakeDown, "final copy") {
		t.Fatalf("with a copy runner the card says %q (%v)", p.TakeDown, err)
	}
	w.nameRunner("")
	p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || p.Operations.FinalCopy.Available || !strings.Contains(p.TakeDown, "refused unless you choose") {
		t.Fatalf("without a copy runner the card says %q (%v)", p.TakeDown, err)
	}
	apps := len(w.api.Snapshot().Apps)
	for door, takeDown := range map[string]func() error{
		"the take-down door": func() error {
			_, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
			return err
		},
		"project deletion": func() error { _, err := w.s.TakeDownWithProject(ctx, w.project, "prod"); return err },
	} {
		err := takeDown()
		if !errors.Is(err, ErrNoFinalCopy) || !errors.Is(err, deployment.ErrNoCopyRunner) || fault.CodeOf(err) != "no-final-copy" || !strings.Contains(err.Error(), deployment.ErrNoCopyRunner.Error()) {
			t.Errorf("%s with no final copy possible: %v", door, err)
		}
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "staging"}); fault.KindOf(err) != fault.Invalid {
		t.Errorf("a choice naming another deployment: %v", err)
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil {
		t.Fatalf("a refused take-down removed the record: %v", err)
	}
	if got := len(w.api.Snapshot().Apps); got != apps {
		t.Fatalf("a refused take-down deleted the app (%d apps, were %d)", got, apps)
	}
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"})
	if err != nil || res.Removed == nil || res.Removed.FinalBackup != nil || len(res.Removed.Retained) == 0 {
		t.Fatalf("the chosen take-down: %+v %v", res, err)
	}
	if deleted := w.api.Snapshot().DeletedClusters; len(deleted) != 0 {
		t.Fatalf("clusters deleted with no final copy: %v", deleted)
	}
	receipts := operationReceipts(t, w.db, "deployment.torn-down", w.project)
	if len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), `"withoutFinalCopy"`) || !strings.Contains(string(receipts[0].Payload), "copy runner") {
		t.Fatalf("the receipt does not record the choice: %+v", receipts)
	}
}

// A choice to go without a final copy, made while none could be taken, is
// refused when it arrives after one became possible: that take-down would
// copy and then delete the cluster, not keep it. Nothing is done, and the
// person confirms again (review CR-01's reproduction).
//
// Kills: running the ordinary take-down on a stale choice to keep the data.
func TestAChoiceToKeepTheDataIsRefusedOnceAFinalCopyIsPossible(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	w.nameRunner("")
	if p, err := w.s.DeploymentProtection(ctx, w.project, "prod"); err != nil || p.Operations.FinalCopy.Available {
		t.Fatalf("without a runner: %+v %v", p, err)
	}
	w.nameRunner("local") // an administrator fixes it while the choice is open
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"})
	if !errors.Is(err, ErrFinalCopyPossible) || fault.CodeOf(err) != "final-copy-possible" || res.Operation != nil || res.Removed != nil {
		t.Fatalf("a stale choice to keep the data: %+v %v", res, err)
	}
	if ops, err := w.opsOn("prod"); err != nil || len(ops) != 0 {
		t.Fatalf("a refused choice started an operation: %+v %v", ops, err)
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil {
		t.Fatalf("a refused choice removed the record: %v", err)
	}
}

// A project being deleted admits, of its deployments' acts, only take-downs
// (taking them down is what a deletion does); a deleted one admits none.
// Each door asks the project's lifecycle under the project's release lock,
// before any effect.
//
// Kills: a copy, drill, release or new deployment admitted in a project
// being deleted; a take-down refused while deleting (the deletion could not
// finish), or admitted once it is deleted.
func TestDeploymentDoorsAskTheProjectsLifecycle(t *testing.T) {
	t.Parallel()
	w, _, _ := releasedProd(t)
	ctx := context.Background()
	if got := w.run(mustOp(t)(w.s.RequestCopy(ctx, w.project, "prod"))); !endedAs(got, OperationNotVerified) {
		t.Fatalf("the copy: %s %s", got.State, got.Detail)
	}
	move := func(to string) {
		t.Helper()
		if err := w.db.InTx(ctx, func(tx *database.Tx) error {
			if to == project.Deleting {
				return project.MarkDeletingIn(ctx, tx, w.project, access.Actor{Name: "alice"}, ids.Nil, false, time.Now())
			}
			return project.MarkDeletedIn(ctx, tx, w.project, time.Now(), time.Now().Add(time.Hour))
		}); err != nil {
			t.Fatal(err)
		}
	}
	doors := map[string]func() error{
		"copy":  func() error { _, err := w.s.RequestCopy(ctx, w.project, "prod"); return err },
		"drill": func() error { _, err := w.s.RequestDrill(ctx, w.project, "prod"); return err },
		"release": func() error {
			_, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: w.bundle.ID})
			return err
		},
		"new deployment": func() error { _, err := w.bind(w.project, "beta", "production", ids.Nil); return err },
		// Workflow completion releases inside its own transaction.
		"release creation": func() error {
			return w.db.InTx(ctx, func(tx *database.Tx) error {
				_, err := deployment.CreateReleaseIn(ctx, tx, deployment.ReleaseDraft{At: time.Now(), Source: deployment.ReleaseFromBranch, ProjectID: w.project, Deployment: "prod", Revision: "abc", Actor: "alice"})
				return err
			})
		},
		"rehearsal": func() error { _, err := w.s.RequestRehearsal(ctx, w.project, "prod", ids.Nil); return err },
		"restored copy": func() error {
			_, err := w.s.RequestRestoredCopy(ctx, w.project, "prod", RestoredCopyRequest{Minutes: 30})
			return err
		},
	}
	move(project.Deleting)
	for name, door := range doors {
		if err := door(); !errors.Is(err, project.ErrNotActive) {
			t.Errorf("%s in a project being deleted: %v", name, err)
		}
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); errors.Is(err, project.ErrNotActive) {
		t.Fatalf("a take-down in a project being deleted: %v", err)
	}
	move(project.Deleted)
	for name, door := range doors {
		if err := door(); !errors.Is(err, project.ErrNotActive) {
			t.Errorf("%s in a deleted project: %v", name, err)
		}
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, project.ErrNotActive) {
		t.Fatalf("a take-down in a deleted project: %v", err)
	}
}

func mustOp(t *testing.T) func(*operations.Operation, error) *operations.Operation {
	return func(op *operations.Operation, err error) *operations.Operation {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
}

// A purged project's copies in the backup store go with it, objects first
// and then their records.
//
// Kills: a purge that leaves the project's store copies behind.
func TestPurgingAProjectRemovesItsCopiesInTheStore(t *testing.T) {
	t.Parallel()
	w, _, _ := releasedProd(t)
	ctx := context.Background()
	if got := w.run(mustOp(t)(w.s.RequestCopy(ctx, w.project, "prod"))); !endedAs(got, OperationNotVerified) {
		t.Fatalf("the copy: %s %s", got.State, got.Detail)
	}
	if len(w.store.Keys("forge-copies")) == 0 {
		t.Fatal("the copy put nothing in the store")
	}
	n, left, err := w.s.PurgeProjectBackups(ctx, w.project, func(context.Context) error { return nil })
	if err != nil || n != 1 || len(left) != 0 {
		t.Fatalf("purge: %d %v %v", n, left, err)
	}
	if keys := w.store.Keys("forge-copies"); len(keys) != 0 {
		t.Fatalf("the store keeps the purged project's copy: %v", keys)
	}
	if sets, err := w.s.backups.ForDeployment(ctx, w.project, "prod", 10); err != nil || len(sets) != 0 {
		t.Fatalf("the purged project's sets: %+v %v", sets, err)
	}
}

// A Docker host's data work asks the project's lifecycle too: in a project
// being deleted no backup is taken (by a person or the schedule), no set is
// restored in place, no missing container is rebuilt and no set is moved to
// the store, each refused before anything reaches the host.
//
// Kills: a backup, restore, scheduled backup, rebuild or off-host move in a
// project being deleted.
func TestDockerDataDoorsAskTheProjectsLifecycle(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "dock", 40107)
	ctx := context.Background()
	kept := w.take("manual")
	w.attach()
	if err := w.s.db.InTx(ctx, func(tx *database.Tx) error {
		return project.MarkDeletingIn(ctx, tx, w.pid, access.Actor{Name: "alice"}, ids.Nil, false, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	w.client.ResetCommands()
	if _, err := w.s.TakeBackup(ctx, w.pid, "main", "manual", "alice"); !errors.Is(err, project.ErrNotActive) {
		t.Fatalf("a backup in a project being deleted: %v", err)
	}
	if _, err := w.s.RestoreBackup(ctx, w.pid, "main", kept.ID, "alice"); !errors.Is(err, project.ErrNotActive) {
		t.Fatalf("a restore in a project being deleted: %v", err)
	}
	dep, err := w.s.Deployments.ByName(ctx, w.pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Rebuild(ctx, dep, deployment.RebuildPlan{}, deploy.Artifact{}); !errors.Is(err, project.ErrNotActive) {
		t.Fatalf("a rebuild in a project being deleted: %v", err)
	}
	got, err := JobWorker{Service: w.s.Service, Projects: project.New(w.s.db, nil)}.Backup(ctx, "dock/main", map[string]any{"trigger": "scheduled"})
	if err != nil || got["skipped"] == nil {
		t.Fatalf("a scheduled backup in a project being deleted: %v %v", got, err)
	}
	if moved, left, err := w.s.MoveSetsToStore(ctx); err != nil || moved != 0 || len(left) != 0 {
		t.Fatalf("the off-host move of a project being deleted: %d %v %v", moved, left, err)
	}
	if cmds := w.client.Commands(); len(cmds) != 0 {
		t.Fatalf("the host was reached: %v", cmds)
	}
	if sets := w.sets(); len(sets) != 1 || sets[0].Store != StoreForgeDisk {
		t.Fatalf("the project's sets: %+v", sets)
	}
}

// A failed or partial set goes with its project too: its files on the disk
// or its objects in the store are removed and its record settled, whatever
// its state, and one that never reached a place is only settled. The purge
// finishes; so does the pass for projects deleted before.
//
// Kills: a failed set the purge can never settle, holding the project for
// ever.
func TestAPurgeRemovesFailedAndPartialSets(t *testing.T) {
	t.Parallel()
	w, _, _ := releasedProd(t)
	ctx := context.Background()
	store, err := w.s.Store(ctx)
	if err != nil || store == nil {
		t.Fatalf("store: %+v %v", store, err)
	}
	root := filepath.Join(w.s.dataDir, "backups")
	disk := filepath.Join(root, "ledger", "prod", "failed")
	if err := os.MkdirAll(disk, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, b := range []Backup{
		{ProjectID: w.project, Deployment: "prod", TakenAt: now, Trigger: "scheduled", Path: disk, Status: BackupFailed, Store: StoreForgeDisk},
		{ProjectID: w.project, Deployment: "prod", TakenAt: now, Trigger: "scheduled", Status: BackupFailed, Store: StoreForgeDisk},
		{ProjectID: w.project, Deployment: "prod", TakenAt: now, Trigger: TriggerTakeDown, Path: "ledger/prod/partial", Status: BackupPartial, Store: StoreBackupStore, StoreBucket: store.Bucket, StoreEndpoint: store.Endpoint},
		{ProjectID: w.project, Deployment: "prod", TakenAt: now, Trigger: TriggerTakeDown, Status: BackupFailed, Store: StoreBackupStore},
		{ProjectID: idstest.N(999999), Deployment: "prod", TakenAt: now, Trigger: "scheduled", Status: BackupFailed, Store: StoreForgeDisk},
	} {
		if _, err := w.s.backups.Record(ctx, &b, "forge-system"); err != nil {
			t.Fatal(err)
		}
	}
	n, left, err := w.s.PurgeProjectBackups(ctx, w.project, func(context.Context) error { return nil })
	if err != nil || n != 4 || len(left) != 0 {
		t.Fatalf("purge: %d %v %v", n, left, err)
	}
	if sets, err := w.s.backups.ForDeployment(ctx, w.project, "prod", 10); err != nil || len(sets) != 0 {
		t.Fatalf("the purged project's sets: %+v %v", sets, err)
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Fatalf("a failed set's files: %v", err)
	}
	if n, left, err := w.s.PurgeOrphanBackups(ctx, time.Now().Add(31*24*time.Hour)); err != nil || n != 1 || len(left) != 0 {
		t.Fatalf("the orphan pass: %d %v %v", n, left, err)
	}
	if sets, err := w.s.backups.ForDeployment(ctx, idstest.N(999999), "prod", 10); err != nil || len(sets) != 0 {
		t.Fatalf("an orphaned failed set: %+v %v", sets, err)
	}
}

// A restore admitted before its project was marked deleting holds its
// deployment while it writes: the deletion's take-down is refused then,
// before it reaches the host, and goes through once the restore ends. A
// restore asked for while a take-down holds the deployment is refused.
//
// Kills: a deletion's final backup or teardown running beside a restore's
// writes.
func TestARestoreUnderWayHoldsADeletionsTakeDown(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "hold", 40108)
	ctx := context.Background()
	set := w.take("manual")
	alice := access.Principal{Kind: "user", Name: "alice", Credential: "token:alice"}
	release, err := w.s.dataWork.claim(mustDep(t, w.s, w.pid, "main").ID, "a take-down of main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RestoreBackup(ctx, w.pid, "main", set.ID, "alice"); err == nil || !strings.Contains(err.Error(), "a take-down of main is under way") {
		t.Fatalf("a restore during a take-down: %v", err)
	}
	release()
	writing, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	setBackupDockerCommand(w.client, func(req docker.Command) ([]byte, error) {
		if req.Stdin != nil {
			// The restore is writing: it waits here, holding the fake
			// daemon, so a take-down that reached the host would hang.
			once.Do(func() { close(writing); <-resume })
			_, err := io.ReadAll(req.Stdin)
			return nil, err
		}
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy")
			return nil, err
		}
		return nil, nil
	})
	restored := make(chan error, 1)
	go func() { _, err := w.s.RestoreBackup(ctx, w.pid, "main", set.ID, "alice"); restored <- err }()
	<-writing
	if err := w.s.db.InTx(ctx, func(tx *database.Tx) error {
		return project.MarkDeletingIn(ctx, tx, w.pid, access.Actor{Name: "alice"}, ids.Nil, false, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	during := make(chan error, 1)
	go func() {
		_, err := w.s.remove(ctx, RemoveDeployment{ProjectID: w.pid, Name: "main", withProject: true}, alice, nil, nil)
		during <- err
	}()
	select {
	case err := <-during:
		if err == nil || !strings.Contains(err.Error(), "a restore of main is under way") {
			t.Fatalf("a take-down during the restore: %v", err)
		}
	case <-time.After(10 * time.Second):
		close(resume)
		t.Fatal("the take-down reached the host while the restore was writing")
	}
	close(resume)
	if err := <-restored; err != nil {
		t.Fatalf("the restore: %v", err)
	}
	// Once the restore has ended the take-down is no longer held (this
	// world's daemon does not model the teardown itself).
	if _, err := w.s.remove(ctx, RemoveDeployment{ProjectID: w.pid, Name: "main", withProject: true}, alice, nil, nil); err != nil && strings.Contains(err.Error(), "under way") {
		t.Fatalf("the take-down once the restore ended: %v", err)
	}
}

func mustDep(t *testing.T, s *both, projectID ids.ID, name string) *deployment.Deployment {
	t.Helper()
	d, err := s.Deployments.ByName(t.Context(), projectID, name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
