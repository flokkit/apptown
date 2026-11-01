package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// finalSet takes a production deployment "prod" down through its final
// copy and answers the set.
func (w *dataWorld) finalSet() *Backup {
	w.t.Helper()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		w.t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		w.t.Fatalf("release %s:\n%s", rel.State, log)
	}
	dep, err := w.s.Deployments.ByName(context.Background(), w.project, "prod")
	if err != nil {
		w.t.Fatal(err)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(id+".fake", "app", "ledger: 41, 42")
	res, err := w.s.TakeDown(context.Background(), RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		w.t.Fatalf("take-down: %+v %v", res, err)
	}
	if op := w.run(res.Operation); !endedAs(op, operationVerified) {
		w.t.Fatalf("take-down %s: %s", op.State, op.Detail)
	}
	finals, err := w.s.FinalCopies(context.Background(), w.project)
	if err != nil || len(finals) != 1 {
		w.t.Fatalf("final copies: %v %v", finals, err)
	}
	set, err := w.s.backups.ByID(context.Background(), w.project, "prod", finals[0].Backup)
	if err != nil {
		w.t.Fatal(err)
	}
	return set
}

// Putting a copy's data into a deployment is a restore, checked where the
// copy came from and where it goes: a caller with only the deploy grant, or
// the restore grant in only one of the two projects, is refused before
// anything is recorded, and so is a production copy into a deployment that
// does not hold production data. The release that would fill a start
// chosen by someone else asks again. (An agent never holds the grant: no
// scope carries it, TestNoScopeCarriesRestore in access.)
//
// Kills: checking only the destination; only the source; the class rule
// missing; the release door not asking again.
func TestStartingFromACopyNeedsRestoreWhereItComesFromAndWhereItGoes(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	set := w.finalSet()
	other := createTestProject(t, w.db, "other", "Other")
	for name, tc := range map[string]struct {
		deny    []string
		project ids.ID
		data    string
		want    error
	}{
		"deploy only":                 {[]string{"deployment:restore"}, other, "production", nil},
		"restore at the source only":  {[]string{fmt.Sprintf("deployment:restore@%d", other)}, other, "production", nil},
		"restore at the destination":  {[]string{fmt.Sprintf("deployment:restore@%d", w.project)}, other, "production", nil},
		"production into development": {nil, other, "non-production", deployment.ErrProductionCopy},
	} {
		w.auth.set("bob", tc.deny...)
		_, err := w.bind(tc.project, "copy", tc.data, set.ID)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := w.s.Deployments.ByName(t.Context(), tc.project, "copy"); err == nil {
			t.Errorf("%s: a refused start recorded a deployment", name)
		}
	}
	// A start is refused when it is chosen on a target with no copy
	// runner to fill it, not when its first release gets there.
	w.auth.set("alice")
	w.nameRunner("")
	if _, err := w.bind(w.project, "prod", "production", set.ID); !errors.Is(err, deployment.ErrNoCopyRunner) {
		t.Fatalf("a start with no copy runner: %v", err)
	}
	if _, err := w.s.Deployments.ByName(t.Context(), w.project, "prod"); err == nil {
		t.Fatal("a start refused for no copy runner recorded a deployment")
	}
	w.nameRunner("local")
	// Chosen by someone who may; released by someone who may only deploy.
	if _, err := w.bind(w.project, "prod", "production", set.ID); err != nil {
		t.Fatal(err)
	}
	w.auth.set("bob", "deployment:restore")
	if _, err := w.s.RequestRelease(context.Background(), deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: w.bundle.ID}); err == nil {
		t.Fatal("a deploy-only caller released a start from a copy")
	}
	w.auth.set("alice")
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
}

// A take-down never deletes a cluster that holds anything but this
// deployment's checked data: an open retained database on
// it, or any database the provider lists that no copy covers and that the
// kind's check does not show empty — the provider's own defaultdb included
// — refuses the deletion. The app is already gone; the data,
// the cluster and the deployment's record stay, and the operation says
// so. Once the other database is dealt with, taking it down again
// finishes.
//
// Kills: deleting the cluster with the retained database in it; ignoring
// the provider's list; removing the deployment's record when the cluster
// stays.
func TestATakeDownNeverDeletesAClusterHoldingOtherData(t *testing.T) {
	t.Parallel()
	for _, other := range []string{"a retained database", "a database of someone else's", "a table in the provider's default database"} {
		t.Run(other, func(t *testing.T) {
			t.Parallel()
			w := newDataWorld(t)
			w.protect()
			ctx := context.Background()
			dep, err := w.bind(w.project, "prod", "production", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			if rel, log := w.release("prod"); rel.State != deployment.Released {
				t.Fatalf("release %s:\n%s", rel.State, log)
			}
			id, _ := w.clusterOf(dep)["id"].(string)
			w.clusters.put(id+".fake", "app", "ledger: 41, 42")
			var settle func()
			switch other {
			case "a retained database":
				// sql-guarantee: a standing restore leaves the displaced database retained on its cluster; that is the next slice, so no operation makes this row yet.
				if _, err := w.db.SQL().ExecContext(context.Background(), `INSERT INTO retained_resources (id,project_id, deployment, target_id, need, handle_json, retained_by, holds_data, retained_at) VALUES ($1,$2, 'prod', $3, 'postgres', $4, 'alice', true, '2026-10-01')`, ids.New(time.Now()), w.project, w.target.ID, fmt.Sprintf(`{"id":%q,"name":%q,"database":"app_before_restore"}`, id, clusterOf(w, "prod"))); err != nil {
					t.Fatal(err)
				}
				rows, err := w.s.Retained.ForProject(ctx, w.project)
				if err != nil || len(rows) != 1 {
					t.Fatalf("retained: %v %v", rows, err)
				}
				settle = func() {
					if err := w.s.SettleRetained(ctx, deployment.SettleRetained{ProjectID: w.project, ResourceID: rows[0].ID, Disposition: deployment.RetainedReleased}); err != nil {
						t.Fatal(err)
					}
				}
			case "a table in the provider's default database":
				// Someone kept a table in defaultdb through the provider's
				// own connection; its name proves nothing about its contents.
				w.clusters.put(id+".fake", "defaultdb", "reports: 7")
				settle = func() { w.clusters.put(id+".fake", "defaultdb", "") }
			default:
				c := digitalocean.NewClient("t", strings.TrimSuffix(w.apiBase, "/"))
				if err := c.Call(ctx, "POST", "/v2/databases/"+id+"/dbs", map[string]any{"name": "reports"}, nil); err != nil {
					t.Fatal(err)
				}
				w.clusters.put(id+".fake", "reports", "monthly: 3")
				settle = func() {
					if err := c.Call(ctx, "DELETE", "/v2/databases/"+id+"/dbs/reports", nil, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
			if err != nil || res.Operation == nil {
				t.Fatalf("take-down: %+v %v", res, err)
			}
			op := w.run(res.Operation)
			if op.State != operations.Failed || !strings.Contains(op.Detail, "app removed, data kept; deploy again to serve") || !strings.Contains(op.Detail, "the cluster "+clusterOf(w, "prod")+" stays") {
				t.Fatalf("take-down %s: %s", op.State, op.Detail)
			}
			if state := w.api.Snapshot(); len(state.DeletedClusters) != 0 || len(state.Apps) != 0 {
				t.Fatalf("deleted clusters %v, apps %d", state.DeletedClusters, len(state.Apps))
			}
			if got, ok := w.clusters.get(id+".fake", "app"); !ok || got != "ledger: 41, 42" {
				t.Fatalf("the data: %q %v", got, ok)
			}
			if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil {
				t.Fatalf("the record went with the cluster still there: %v", err)
			}
			settle()
			res, err = w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
			if err != nil || res.Operation == nil {
				t.Fatalf("take-down again: %+v %v", res, err)
			}
			if op := w.run(res.Operation); !endedAs(op, operationVerified) {
				t.Fatalf("take-down again %s: %s", op.State, op.Detail)
			}
			if state := w.api.Snapshot(); len(state.DeletedClusters) != 1 {
				t.Fatalf("deleted clusters %v", state.DeletedClusters)
			}
		})
	}
}

// A final copy that cannot be taken deletes nothing more: the app is gone,
// the cluster and its data stay, the deployment's record stands so it can
// be deployed again, and the operation reads failed with where the data
// is. Deploying again serves the same data.
//
// Kills: deleting the cluster after a failed copy; removing the record.
func TestAFailedFinalCopyKeepsTheData(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(id+".fake", "app", "ledger: 41, 42")
	w.clusters.failExport = true
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down: %+v %v", res, err)
	}
	op := w.run(res.Operation)
	if op.State != operations.Failed || !strings.HasPrefix(op.Detail, "app removed, data kept; deploy again to serve.") {
		t.Fatalf("take-down %s: %s", op.State, op.Detail)
	}
	if state := w.api.Snapshot(); len(state.DeletedClusters) != 0 || len(state.Apps) != 0 {
		t.Fatalf("deleted clusters %v, apps %d", state.DeletedClusters, len(state.Apps))
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil {
		t.Fatal(err)
	}
	w.clusters.failExport = false
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("deploying again %s:\n%s", rel.State, log)
	}
	if got, _ := w.clusters.get(id+".fake", "app"); got != "ledger: 41, 42" {
		t.Fatalf("the data after deploying again: %q", got)
	}
}

// A data operation is durable and single: asking again while one is live
// names it (a 409 with its link), a take-down is refused beside a copy,
// and nothing deploys an app a take-down is deleting. A restart leaves a
// queued copy for its job, which runs it (the restart rules themselves
// are the operations owner's: TestARestartSettlesRunningUnknownAndRequeuesQueued).
//
// Kills: a second operation beside the first; a take-down beside a copy;
// a release beside a take-down; a restart that loses a queued operation.
func TestADataOperationIsSingleAndKeepsReleasesAwayFromATakeDown(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	first, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var running *operations.HeldError
	if _, err := w.s.RequestCopy(ctx, w.project, "prod"); !errors.As(err, &running) || running.Holder.ID != first.ID || fault.KindOf(err) != fault.Conflict {
		t.Fatalf("a second copy: %v", err)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.As(err, &running) {
		t.Fatalf("a take-down beside a copy: %v", err)
	}
	if r, err := w.restart(); err != nil || r.Settled != 0 || r.Requeued != 0 {
		t.Fatalf("a restart touched a queued operation whose job waits: %+v %v", r, err)
	}
	if got := w.run(first); !endedAs(got, operationNotVerified) {
		t.Fatalf("the queued copy, run by its job: %s %s", got.State, got.Detail)
	}
	// A take-down that is live keeps releases away.
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down: %+v %v", res, err)
	}
	if _, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: w.bundle.ID}); !errors.As(err, &running) {
		t.Fatalf("a release during a take-down: %v", err)
	}
}

// What the deployment page says of each data-holding item: AppTown's
// newest copy and how recent it is, the provider's own backups as the
// provider says now, and whether a copy was seen to come back. Production
// data with no store reads "not protected off-host"; a copy that failed
// after a good one reads stale; a provider that cannot be asked reads
// unknown with why, never none.
//
// Kills: fresh from a failed copy; none for an unreadable provider; a drill
// line from a copy that did not come back.
func TestTheDeploymentSaysHowItsDataIsProtected(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.s.nativeFresh = 0 // ask the provider every time
	ctx := context.Background()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(id+".fake", "app", "ledger: 41")
	p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || p.OffHost || p.Note != "not protected off-host: no backup store is attached" || len(p.Items) != 1 || p.Items[0].Copy != CopyNone {
		t.Fatalf("before a store: %+v %v", p, err)
	}
	w.protect()
	w.api.SeedNativeBackups(id, "2026-10-01T06:10:00Z", "2026-10-07T06:10:00Z")
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); !endedAs(got, operationNotVerified) {
		t.Fatalf("copy %s: %s", got.State, got.Detail)
	}
	p, err = w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	it := p.Items[0]
	if !p.OffHost || it.Copy != CopyFresh || it.LastCopy == nil || it.Drill != nil || it.Native == nil || it.Native.Count != 2 || it.Native.Newest.Time.Day() != 7 || it.Native.Unknown != "" {
		t.Fatalf("after a copy: %+v %+v", p, it)
	}
	// A fresh copy covers its database, named by its place; data outside
	// the deployment stays not assessed.
	if !slices.Equal(p.Coverage.Assessed, []string{"postgres · " + it.Where}) || !slices.Equal(p.Coverage.NotAssessed, []string{outsideNotAssessed}) {
		t.Fatalf("coverage after a copy: %+v", p.Coverage)
	}
	drill, err := w.s.RequestDrill(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(drill); !endedAs(got, operationVerified) {
		t.Fatalf("drill %s: %s", got.State, got.Detail)
	}
	if dbs, _ := w.s.providers.ManagedData(w.target).Databases(ctx, id); len(dbs) != 2 {
		t.Fatalf("the drill left a database behind: %v", dbs)
	}
	p, _ = w.s.DeploymentProtection(ctx, w.project, "prod")
	if d := p.Items[0].Drill; d == nil || !d.Passed || d.User != "app" || d.Target != "do-syd" || d.Backup != it.LastCopy.Backup {
		t.Fatalf("drill line: %+v", d)
	}
	// The next copy fails: stale, with why.
	w.clusters.failExport = true
	op, err = w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); got.State != operations.Failed {
		t.Fatalf("failing copy %s", got.State)
	}
	w.api.Fail("GET", "/v2/databases/"+id+"/backups", 500)
	p, _ = w.s.DeploymentProtection(ctx, w.project, "prod")
	if it := p.Items[0]; it.Copy != CopyStale || !strings.Contains(it.CopyNote, "the last copy failed") || it.Native == nil || it.Native.Unknown != "" || !it.Native.CheckFailed {
		t.Fatalf("after a failed copy: %+v", it)
	}
	if state, _ := copyState(&CopyRef{TakenAt: time.Now().Add(-31 * time.Hour).Format(time.RFC3339)}, nil, time.Now()); state != CopyStale {
		t.Fatalf("a copy 31 hours old reads %s", state)
	}
	// What it holds cannot be read: unknown, never an error and never "no
	// data"; no copy, check or final copy reads available, and the copy's
	// own door refuses.
	dbtest.FailNextStatement(t, w.db, "-- name: NeedInstances")
	p, err = w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || p.Unknown == "" || len(p.Items) != 0 || p.TakeDown != "" || p.Operations == nil ||
		p.Operations.Copy.Available || p.Operations.Check.Available || p.Operations.FinalCopy.Available ||
		!strings.Contains(p.Operations.Copy.Why, "not known") || !strings.Contains(p.Operations.FinalCopy.Why, "not known") {
		t.Fatalf("an unread list: %+v %+v %v", p.Operations, p.Unknown, err)
	}
	dbtest.FailNextStatement(t, w.db, "-- name: NeedInstances")
	if _, err := w.s.RequestCopy(ctx, w.project, "prod"); !errors.Is(err, errDataNotKnown) {
		t.Fatalf("a copy of an unread list: %v", err)
	}
}

// A deployment's protection says, for each operation on its data, whether
// its own door would admit it now, and when not, why, in the words and code
// that door refuses with: no store, then no copy runner, then (with both)
// a check that waits for a first copy. The card greys each button from its
// own answer, so each must match its refusal.
//
// Kills: reporting a copy available without a copy runner; a reason the
// refusal does not give; a check offered before there is any copy; one
// shared flag for every operation.
func TestTheDeploymentSaysWhyEachDataOperationCannotStart(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || p.Target != "do-syd" || p.Operations == nil || p.Operations.Copy.Available || p.Operations.Copy.Reason != "no-store" {
		t.Fatalf("before a store: %+v %v", p.Operations, err)
	}
	w.protect()
	w.nameRunner("")
	p, err = w.s.DeploymentProtection(ctx, w.project, "prod")
	ops := p.Operations
	if err != nil || p.OffHost || ops == nil || ops.Copy.Available || ops.Copy.Reason != "no-copy-runner" || ops.FinalCopy.Reason != "no-copy-runner" || ops.StartFromCopy.Reason != "no-copy-runner" {
		t.Fatalf("without a copy runner: %+v %v", ops, err)
	}
	if c := CopiesOn(w.target); c == nil || c.Available || c.Reason != ops.Copy.Reason || c.Why != ops.Copy.Why {
		t.Fatalf("the target without a copy runner says %+v, the deployment %+v", c, ops.Copy)
	}
	for door, tc := range map[string]struct {
		said    Availability
		request func() error
	}{
		"copy":  {ops.Copy, func() error { _, err := w.s.RequestCopy(ctx, w.project, "prod"); return err }},
		"check": {ops.Check, func() error { _, err := w.s.RequestDrill(ctx, w.project, "prod"); return err }},
	} {
		err := tc.request()
		if !errors.Is(err, deployment.ErrNoCopyRunner) || err.Error() != tc.said.Why || fault.CodeOf(err) != tc.said.Reason {
			t.Errorf("%s without a copy runner: %v (the card says %+v)", door, err, tc.said)
		}
	}
	// With a store and a runner, before the first copy: a copy can start,
	// a check cannot yet, and says so as its door does.
	w.nameRunner("local")
	p, err = w.s.DeploymentProtection(ctx, w.project, "prod")
	ops = p.Operations
	if err != nil || !p.OffHost || !ops.Copy.Available || ops.Copy.Why != "" || !ops.FinalCopy.Available || !ops.StartFromCopy.Available || ops.Check.Available || ops.Check.Reason != "no-copy" {
		t.Fatalf("with a copy runner, before a copy: %+v %v", ops, err)
	}
	if _, err := w.s.RequestDrill(ctx, w.project, "prod"); !errors.Is(err, errNoCopy) || err.Error() != ops.Check.Why {
		t.Fatalf("a check before any copy: %v (the card says %+v)", err, ops.Check)
	}
	if c := CopiesOn(w.target); c == nil || !c.Available {
		t.Fatalf("the target with a copy runner says %+v", c)
	}
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); !endedAs(got, operationNotVerified) {
		t.Fatalf("copy %s: %s", got.State, got.Detail)
	}
	if p, err = w.s.DeploymentProtection(ctx, w.project, "prod"); err != nil || !p.Operations.Check.Available {
		t.Fatalf("after a copy the check reads %+v %v", p.Operations.Check, err)
	}
}

// The store is attached only after it took a private object, gave it back
// and deleted it; a bucket the keys cannot reach attaches nothing.
func TestTheBackupStoreIsAttachedOnlyAfterItsCheck(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := context.Background()
	if _, err := w.s.AttachStore(ctx, AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "no-such-bucket", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); !errors.Is(err, errStoreCheck) {
		t.Fatalf("a missing bucket: %v", err)
	}
	if _, err := w.s.AttachStore(ctx, AttachStore{Endpoint: "not a url", Region: "syd1", Bucket: "forge-copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); !errors.Is(err, errStoreInput) {
		t.Fatalf("a bad endpoint: %v", err)
	}
	w.auth.set("bob", "platform:manage-deploy-targets")
	if _, err := w.s.AttachStore(ctx, AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "forge-copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); err == nil {
		t.Fatal("a non-administrator attached the store")
	}
	if st, err := w.s.Store(ctx); err != nil || st != nil {
		t.Fatalf("a refused attach left %+v %v", st, err)
	}
	w.auth.set("alice")
	st, err := w.s.AttachStore(ctx, AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "forge-copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"})
	if err != nil || st.Bucket != "forge-copies" || st.AttachedBy != "alice" {
		t.Fatalf("attach: %+v %v", st, err)
	}
	if keys := w.store.Keys("forge-copies"); len(keys) != 0 {
		t.Fatalf("the check left %v", keys)
	}
	var wrote bool
	for _, c := range w.store.Calls() {
		wrote = wrote || (c.Method == "PUT" && strings.HasPrefix(c.Key, ".apptown-check/"))
	}
	if !wrote {
		t.Fatal("the check never wrote")
	}
}

// A set in the backup store is filled only into a new place: restoring it
// over a standing deployment's data is refused before anything changes.
func TestAStoreCopyIsNeverRestoredInPlace(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	set := w.finalSet()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	_, err := w.s.restoreBackup(context.Background(), nil, w.project, "prod", set.ID, "alice")
	if !errors.Is(err, errRestoreMismatch) || !strings.Contains(err.Error(), "start a new deployment from it") {
		t.Fatalf("restore in place: %v", err)
	}
	if unchecked, err := w.s.uncheckedItems(context.Background(), set); err != nil || len(unchecked) != 0 {
		t.Fatalf("the final set's checks after a refused restore: %v %v", unchecked, err)
	}
}

// The provider's backups are asked about at most once in NativeReadFor per
// instance: a deployment's card reads its protection every few seconds, and
// the provider's API has a rate limit.
func TestTheProvidersBackupsAreNotAskedOnEveryRead(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	first := now
	w.s.clock = func() time.Time { return now }
	for range 3 {
		protection, err := w.s.DeploymentProtection(ctx, w.project, "prod")
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, item := range protection.Items {
			if item.Native != nil {
				seen = true
				if item.Native.ObservedAt != first {
					t.Fatalf("cached provider observation time: %v, want %v", item.Native.ObservedAt, first)
				}
			}
		}
		if !seen {
			t.Fatal("no provider backup evidence")
		}
		now = now.Add(time.Minute)
	}
	asked := 0
	for _, r := range w.api.Snapshot().Requests {
		if r.Method == "GET" && r.Path == "/v2/databases/"+id+"/backups" {
			asked++
		}
	}
	if asked != 1 {
		t.Fatalf("asked the provider %d times", asked)
	}
	// The background observer uses the real held-data/protection owner, and
	// cannot renew native evidence retained by that owner's inner cache.
	q := &protectionObservationRequests{}
	observer := NewProtectionObserver(w.s.Service, q)
	cold, err := observer.Snapshot(ctx, dep.ID)
	if err != nil || !cold.Unknown || q.calls != 0 {
		t.Fatalf("cold protection: %+v %v", cold, err)
	}
	_, identity, err := observer.resolve(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.run(ctx, dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	warm, err := observer.Snapshot(ctx, dep.ID)
	if err != nil || warm.ObservedAt.IsZero() || len(warm.Items) == 0 || warm.Stale {
		t.Fatalf("observed protection lost inner age: %+v %v", warm, err)
	}
	seen := false
	for _, item := range warm.Items {
		if item.Native {
			seen = true
			if item.NativeAt != first {
				t.Fatalf("native evidence renewed: %+v", item)
			}
		}
	}
	if !seen {
		t.Fatal("held native item omitted")
	}
}

// A take-down and a release of the same deployment never both get
// in. A release request that has looked and seen no take-down, then waits
// on its source while a take-down starts, is refused when it is admitted:
// the two are decided under the same lock. A take-down asked while a
// release is in flight is refused the same way.
func TestATakeDownAndAReleaseNeverBothGetIn(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	var started *operations.Operation
	w.during = func() {
		res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
		if err != nil || res.Operation == nil {
			t.Errorf("take-down in the middle: %+v %v", res, err)
		}
		started = res.Operation
	}
	_, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: w.bundle.ID})
	var running *operations.HeldError
	if !errors.As(err, &running) || started == nil || running.Holder.ID != started.ID {
		t.Fatalf("a release admitted beside a take-down: %v", err)
	}
	if op := w.run(started); !endedAs(op, operationVerified) {
		t.Fatalf("take-down %s: %s", op.State, op.Detail)
	}

	// The other order: a release in flight keeps a take-down out.
	if _, err := w.bind(w.project, "next", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("next"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	if _, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "next", BundleID: w.bundle.ID}); err != nil {
		t.Fatal(err)
	}
	if res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "next"}); err == nil || res.Operation != nil {
		t.Fatalf("a take-down admitted beside a release in flight: %+v", res)
	}
}

// The release that fills a start from a copy fills only the copy
// its own caller was authorised for. A start chosen while a release is
// queued is refused; and a release fills only the set recorded when it was
// admitted, never one chosen since.
func TestAQueuedReleaseFillsOnlyTheCopyItWasAuthorisedFor(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	set := w.finalSet()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	// A deploy-only caller's release waits in the queue.
	w.auth.set("bob", "deployment:restore")
	accepted, err := w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: w.bundle.ID})
	if err != nil {
		t.Fatal(err)
	}
	// An owner chooses a start meanwhile.
	w.auth.set("alice")
	if _, err := w.bind(w.project, "prod", "production", set.ID); !errors.Is(err, deployment.ErrStartFrom) {
		t.Fatalf("a start chosen under a queued release: %v", err)
	}
	// Even were the selection changed by other means, the queued release
	// holds to what it was admitted with.
	dep, _ := w.s.Deployments.ByName(ctx, w.project, "prod")
	// sql-guarantee: no operation changes a start while a release is in flight; this proves the fill holds to its release's record anyway.
	if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE deployments SET start_from_backup=$1 WHERE id=$2`, set.ID, dep.ID); err != nil {
		t.Fatal(err)
	}
	_ = w.s.ExecuteRelease(ctx, accepted.ID)
	rel, err := w.s.Releases.ByNumber(ctx, w.project, accepted.Number)
	if err != nil {
		t.Fatal(err)
	}
	if rel.State == deployment.Released {
		t.Fatal("a release filled a copy its caller was never authorised for")
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	if got, _ := w.clusters.get(id+".fake", "app"); got != "" {
		t.Fatalf("the unauthorised copy went in: %q", got)
	}
}

// A set is read from where it was written. Each set records the
// store's endpoint and bucket; attaching another bucket while sets still
// live in the attached one is refused (they would be read, pruned and
// offered from the wrong place), and the same bucket may be attached again
// with other key names. A set whose recorded place is not the attached
// store is never read from the attached one.
func TestAStoreIsNotReplacedUnderTheSetsItHolds(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	set := w.finalSet()
	ctx := context.Background()
	if set.StoreBucket != "forge-copies" || set.StoreEndpoint != w.store.API.URL {
		t.Fatalf("the set does not say where it is: %+v", set)
	}
	w.store.AddBucket("elsewhere")
	if _, err := w.s.AttachStore(ctx, AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "elsewhere", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); !errors.Is(err, errStoreHoldsSets) {
		t.Fatalf("another bucket attached over a held set: %v", err)
	}
	if st, _ := w.s.Store(ctx); st.Bucket != "forge-copies" {
		t.Fatalf("the store moved: %+v", st)
	}
	if _, err := w.s.AttachStore(ctx, AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "forge-copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); err != nil {
		t.Fatalf("the same bucket again: %v", err)
	}
	// sql-guarantee: no operation moves the store under held sets; this proves a set is never read from a store it was not written to.
	if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE backup_store SET bucket='elsewhere'`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bind(w.project, "prod", "production", set.ID); err != nil {
		t.Fatal(err)
	}
	rel, log := w.release("prod")
	if rel.State == deployment.Released || !strings.Contains(log, "set "+set.ID.String()+" is in the bucket forge-copies") {
		t.Fatalf("a set read from a store it was not written to: %s\n%s", rel.State, log)
	}
}

// A manifest that cannot be written beside the copies is said on the set's
// record, which is written first so the manifest can name it.
//
// Kills: the manifest's failure dropped once the set is recorded first.
func TestAManifestThatCouldNotBeWrittenIsSaidOnTheSet(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	w.store.Fail("PUT", "forge-copies", "*/manifest.json", 500)
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	w.run(op)
	sets, err := w.s.backups.ForDeployment(context.Background(), w.project, "prod", 5)
	if err != nil || len(sets) != 1 || !strings.Contains(sets[0].Error, "the manifest could not be written beside the copies") {
		t.Fatalf("sets: %+v %v", sets, err)
	}
}

// A manifest upload cut short by its operation's context ending (a
// shutdown, the operation's budget) is said on the recorded set all the
// same: the caveat is written after the upload's context has ended.
func TestAManifestCutShortByCancellationIsSaidOnTheSet(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	op, err := w.s.RequestCopy(context.Background(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.store.SetBefore(func(c s3fake.Call) {
		if c.Method == "PUT" && strings.HasSuffix(c.Key, "manifest.json") {
			cancel()
			// The PUT is already in flight, so the cancellation alone may
			// lose the race and let it land (it did under -race in CI).
			// Failing it too makes the cut deterministic; the context is
			// cancelled before the answer either way.
			w.store.Fail(c.Method, c.Bucket, c.Key, 500)
		}
	})
	_, _ = w.ops.Run(ctx, op.ID)
	sets, err := w.s.backups.ForDeployment(context.Background(), w.project, "prod", 5)
	if err != nil || len(sets) != 1 || !strings.Contains(sets[0].Error, "the manifest could not be written beside the copies") {
		t.Fatalf("sets: %+v %v", sets, err)
	}
}

// A take-down asks again, before it deletes the app and before it deletes
// each cluster, whether the credential that admitted it still stands: one
// revoked after admission deletes nothing, and the operation says why. A
// copy, which holds only the deployment's data, does not keep a release
// out, as it did not before operations had holds.
//
// Kills: checking authority only at admission; a data hold that also
// blocks releases.
func TestATakeDownWhoseAuthorityWasWithdrawnDeletesNothing(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	copying, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("a release beside a copy %s:\n%s", rel.State, log)
	}
	if got := w.run(copying); !endedAs(got, operationNotVerified) {
		t.Fatalf("the copy: %s %s", got.State, got.Detail)
	}
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down: %+v %v", res, err)
	}
	apps := len(w.api.Snapshot().Apps)
	w.auth.revoke("token:alice")
	got := w.run(res.Operation)
	if got.State != operations.Failed || !strings.Contains(got.Detail, "authority withdrawn") || !strings.Contains(got.Detail, "nothing was changed") {
		t.Fatalf("a take-down whose credential was revoked: %s %q", got.State, got.Detail)
	}
	if state := w.api.Snapshot(); len(state.Apps) != apps || len(state.DeletedClusters) != 0 {
		t.Fatalf("a refused take-down deleted: apps %d (was %d), clusters %v", len(state.Apps), apps, state.DeletedClusters)
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil {
		t.Fatalf("the deployment's record went: %v", err)
	}
}

// A copy that fails after a good one makes the deployment's protection
// stale with why, whatever the clocks: with the operations ledger's clock
// (which times operations) eleven minutes behind or ahead of AppTown's
// (which times copies), the failed copy is still the last word.
//
// Kills: ordering the failed operation against the good copy by their
// times, which come from different clocks.
func TestALaterFailedCopyIsStaleWhateverTheClocks(t *testing.T) {
	t.Parallel()
	for _, by := range []time.Duration{-11 * time.Minute, 11 * time.Minute} {
		t.Run(by.String(), func(t *testing.T) {
			t.Parallel()
			w := newDataWorldWith(t, dataWorldOptions{opsNow: func() time.Time { return time.Now().Add(by) }})
			ctx := context.Background()
			dep, err := w.bind(w.project, "prod", "production", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			if rel, _ := w.release("prod"); rel.State != deployment.Released {
				t.Fatal(rel.State)
			}
			id, _ := w.clusterOf(dep)["id"].(string)
			w.clusters.put(id+".fake", "app", "ledger: 41")
			w.protect()
			for _, fail := range []bool{false, true} {
				w.clusters.failExport = fail
				op, err := w.s.RequestCopy(ctx, w.project, "prod")
				if err != nil {
					t.Fatal(err)
				}
				if got := w.run(op); (got.State == operations.Failed) != fail {
					t.Fatalf("copy (failing %v): %s %s", fail, got.State, got.Detail)
				}
			}
			p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			if it := p.Items[0]; it.LastCopy == nil || it.Copy != CopyStale || !strings.Contains(it.CopyNote, "the last copy failed") {
				t.Fatalf("after a good copy and a later failed one: %+v", it)
			}
		})
	}
}

// A failed last copy stays the last word however much other work follows
// it on the deployment: after more drills than the card's short history
// shows, protection still reads stale with why, and only a good copy makes
// it fresh again.
//
// Kills: the last copy read from the card's history of recent operations.
func TestAFailedCopyStaysTheLastWordAfterOtherWork(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := context.Background()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(id+".fake", "app", "ledger: 41")
	w.protect()
	copyOnce := func(fail bool) {
		t.Helper()
		w.clusters.failExport = fail
		op, err := w.s.RequestCopy(ctx, w.project, "prod")
		if err != nil {
			t.Fatal(err)
		}
		if got := w.run(op); (got.State == operations.Failed) != fail {
			t.Fatalf("copy (failing %v): %s %s", fail, got.State, got.Detail)
		}
	}
	protection := func() (*Protection, ItemProtection) {
		t.Helper()
		p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
		if err != nil || len(p.Items) != 1 {
			t.Fatalf("protection: %+v %v", p, err)
		}
		return p, p.Items[0]
	}
	copyOnce(false)
	copyOnce(true)
	// Drills until the card's history shows no copy at all.
	showsACopy := func(p *Protection) bool {
		for _, op := range p.Recent {
			if op.Kind == operationCopy {
				return true
			}
		}
		return false
	}
	p, it := protection()
	for n := 0; showsACopy(p); n++ {
		if n == 20 {
			t.Fatal("the card's history kept a copy through twenty drills")
		}
		drill, err := w.s.RequestDrill(ctx, w.project, "prod")
		if err != nil {
			t.Fatal(err)
		}
		if got := w.run(drill); !endedAs(got, operationVerified) {
			t.Fatalf("drill %s: %s", got.State, got.Detail)
		}
		p, it = protection()
	}
	if it.Copy != CopyStale || !strings.Contains(it.CopyNote, "the last copy failed") {
		t.Fatalf("a failed copy pushed out of the history by later drills: %+v", it)
	}
	copyOnce(false)
	if _, it := protection(); it.Copy != CopyFresh {
		t.Fatalf("a good copy after the failure: %+v", it)
	}
}

// A drill's time is written and judged by one clock, AppTown's: with that
// clock a month off the wall clock, a production deployment never drilled
// is due, a drill just passed leaves the next one not due, and eight days
// later by the same clock it is due.
//
// Kills: the drill's time written from the wall clock (due at once), or
// judged by it (never due); a deployment with no drill read as not due.
func TestADrillIsDueByAppTownsOwnClock(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := context.Background()
	var ahead atomic.Int64
	ahead.Store(int64(30 * 24 * time.Hour))
	w.s.clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(id+".fake", "app", "ledger: 41")
	w.protect()
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); !endedAs(got, operationNotVerified) {
		t.Fatalf("copy %s: %s", got.State, got.Detail)
	}
	set, err := w.s.newestStoreSet(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !w.s.drillDue(ctx, dep, set) {
		t.Fatal("a production deployment never drilled is not due")
	}
	drill, err := w.s.RequestDrill(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(drill); !endedAs(got, operationVerified) {
		t.Fatalf("drill %s: %s", got.State, got.Detail)
	}
	if w.s.drillDue(ctx, dep, set) {
		t.Fatal("a drill just passed left the next one due")
	}
	ahead.Add(int64(8 * 24 * time.Hour))
	if !w.s.drillDue(ctx, dep, set) {
		t.Fatal("eight days after a drill, by AppTown's clock, the next is not due")
	}
}

// A managed instance AppTown cannot copy (here a first fulfilment that
// stopped once the provider had made the cluster, before its database and
// user: the record names an instance and no database) stays in the
// deployment's list: a protection row that says why, never left out, and a
// take-down that keeps the cluster rather than counting the rest as covered.
func TestAManagedInstanceAppTownCannotCopyStaysInTheList(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := context.Background()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	w.protect()
	requested := map[string]any{"name": "forge-ledger-prod", "engine": "pg", "database": "app", "user": "app"}
	if err := deployment.NewNeedStore(w.db, nil).RecordIntent(ctx, dep.ID, "postgres", requested); err != nil {
		t.Fatal(err)
	}
	if err := deployment.NewNeedStore(w.db, nil).RecordIdentity(ctx, dep.ID, "postgres", requested, "db-stopped", "forge-ledger-prod"); err != nil {
		t.Fatal(err)
	}
	p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || p.Unknown != "" || len(p.Items) != 1 || p.Items[0].Copy != CopyNone || !strings.Contains(p.Items[0].CopyNote, "names no instance") || p.Operations.FinalCopy.Available {
		t.Fatalf("an instance AppTown cannot copy: %+v %v", p, err)
	}
	if _, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"}); !errors.Is(err, errNoFinalCopy) {
		t.Fatalf("a take-down with nothing it can copy: %v", err)
	}
	if deleted := w.api.Snapshot().DeletedClusters; len(deleted) != 0 {
		t.Fatalf("clusters deleted: %v", deleted)
	}
}

type protectionObservationRequests struct{ calls int }

func (q *protectionObservationRequests) RequestProtectionObservation(context.Context, ids.ID, string) error {
	q.calls++
	return nil
}

func TestProtectionSnapshotsRetainWarningsAndRejectReplacedGenerations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	first := now
	dep := deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), Kind: deployment.Standing, Name: "production"}
	identity := strings.Repeat("a", 64)
	q := &protectionObservationRequests{}
	calls := 0
	fail, replace := false, false
	result := &Protection{DataClass: "production", Items: []ItemProtection{{Need: "database", Copy: CopyStale, Volume: "/private-store", Native: &NativeBackups{ObservedAt: first, Count: 1}}}, Coverage: Coverage{NotAssessed: []string{outsideNotAssessed}}}
	o := &ProtectionObserver{db: testDB(t), queue: q, now: func() time.Time { return now }, resolve: func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return &dep, identity, nil }, read: func(context.Context, ids.ID, string) (*Protection, error) {
		calls++
		if replace {
			identity = strings.Repeat("b", 64)
		}
		if fail {
			return nil, errors.New("provider secret")
		}
		return result, nil
	}}
	for range 3 {
		got, err := o.Snapshot(t.Context(), dep.ID)
		if err != nil || !got.Unknown || !got.Stale || !got.AttemptedAt.IsZero() || calls != 0 || q.calls != 0 {
			t.Fatalf("cold read: %+v %v", got, err)
		}
	}
	if err := o.run(t.Context(), dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	got, err := o.Snapshot(t.Context(), dep.ID)
	if err != nil || got.Unknown || got.Stale || got.AttemptedAt != now || len(got.Items) != 1 || got.Items[0].Copy != CopyStale || strings.Contains(got.Items[0].Key, "private") {
		t.Fatalf("first evidence: %+v %v", got, err)
	}
	got.Items[0].Copy = CopyFresh
	now = now.Add(30 * time.Minute)
	if err := o.run(t.Context(), dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	got, err = o.Snapshot(t.Context(), dep.ID)
	if err != nil || !got.Stale || got.Items[0].NativeAt != first || got.Items[0].Copy != CopyStale || got.ObservedAt != now {
		t.Fatalf("wrapper renewed native evidence or caller changed owner: %+v %v", got, err)
	}
	fail = true
	now = now.Add(time.Minute)
	if err := o.run(t.Context(), dep.ID, identity); err == nil {
		t.Fatal("provider error lost")
	}
	got, err = o.Snapshot(t.Context(), dep.ID)
	if err != nil || got.Unknown || got.AttemptedAt != now || len(got.Items) != 1 || got.Items[0].Copy != CopyStale {
		t.Fatalf("failure erased warning: %+v %v", got, err)
	}
	fail = false
	replace = true
	old := identity
	if err := o.run(t.Context(), dep.ID, old); err != nil {
		t.Fatal(err)
	}
	got, err = o.Snapshot(t.Context(), dep.ID)
	if err != nil || !got.Unknown || !got.AttemptedAt.IsZero() || len(got.Items) != 0 {
		t.Fatalf("old generation published under replacement: %+v %v", got, err)
	}
	replace = false
	result.Items[0].Copy = CopyFresh
	result.Items[0].Native.ObservedAt = now
	if err := o.run(t.Context(), dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	got, err = o.Snapshot(t.Context(), dep.ID)
	if err != nil || got.Unknown || got.Stale || got.Items[0].Copy != CopyFresh {
		t.Fatalf("recovery not observed: %+v %v", got, err)
	}
}

func TestProtectionSnapshotsDoNotWaitForProviderAndRefuseCanceledPublication(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	dep := deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), Kind: deployment.Standing, Name: "production"}
	identity := strings.Repeat("a", 64)
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q := &protectionObservationRequests{}
	o := &ProtectionObserver{db: testDB(t), queue: q, now: func() time.Time { return now }, resolve: func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return &dep, identity, nil }, read: func(ctx context.Context, _ ids.ID, _ string) (*Protection, error) {
		close(started)
		<-ctx.Done()
		// Even a misbehaving adapter returning success after cancellation cannot
		// publish a new reading under an ended job lifetime.
		return &Protection{}, nil
	}}
	if _, err := o.Snapshot(t.Context(), dep.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- o.run(ctx, dep.ID, identity) }()
	<-started
	got, err := o.Snapshot(t.Context(), dep.ID)
	if err != nil || !got.Unknown || !got.AttemptedAt.IsZero() || q.calls != 0 {
		t.Fatalf("blocked read affected snapshot: %+v %v", got, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	got, err = o.Snapshot(t.Context(), dep.ID)
	if err != nil || !got.Unknown || !got.ObservedAt.IsZero() || got.AttemptedAt != now {
		t.Fatalf("canceled result published: %+v %v", got, err)
	}
}

func TestProtectionObservationRefusesMalformedIdentityBeforeReads(t *testing.T) {
	t.Parallel()
	o := &ProtectionObserver{}
	for _, key := range []string{"", "bad/" + strings.Repeat("a", 64), idstest.N(1).String() + "/short", idstest.N(1).String() + "/" + strings.Repeat("z", 64)} {
		if _, err := o.RunJob(t.Context(), key, nil); err == nil {
			t.Fatalf("accepted invalid job identity %q", key)
		}
	}
}
