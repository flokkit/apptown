package dataprotection

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/s3bucket/s3fake"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/ids"
)

const bucketOfProd = "forge-ledger-prod-b0b0b0b0"

// heldOnAppPlatform releases prod, needing what contract declares, on the
// App Platform target, its data production.
func heldOnAppPlatform(t *testing.T, contract string) *dataWorld {
	t.Helper()
	w := newDataWorldWith(t, dataWorldOptions{contract: []byte(contract)})
	w.protect()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	return w
}

const bucketOfCopy = "forge-ledger-copy-b0b0b0b0"

// putObjects puts objects in a bucket at the account's Spaces, as the app
// would.
func putObjects(w *dataWorld, bucket string) {
	w.store.Put(bucket, "invoices/1.pdf", s3fake.Object{Body: []byte("%PDF one"), ContentType: "application/pdf"})
	w.store.Put(bucket, "logo.png", s3fake.Object{Body: []byte("PNG"), ContentType: "image/png"})
}

// What an App Platform deployment holds includes its bucket, copied like a
// database: the data view lists it from its copies, and a take-down stops
// the app first, copies the bucket as the deployment's own key (which it
// keeps until then), checks the copy by filling a new store of the kind on
// the copy runner, and only then deletes the cluster, the key and the
// bucket. Its final copy fills a deployment started from it: the new
// deployment's own bucket holds the objects, checked.
func TestATakeDownCopiesAndChecksTheBucketBeforeDeletingIt(t *testing.T) {
	t.Parallel()
	w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n  s3: {}\n")
	ctx := context.Background()
	putObjects(w, bucketOfProd)
	prot, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var bucketRow *ItemProtection
	for i := range prot.Items {
		if prot.Items[i].Need == "s3" {
			bucketRow = &prot.Items[i]
		}
	}
	// Not yet copied, and assessed like the database: no "the forge takes
	// no copy" caveat any more.
	if len(prot.Items) != 2 || bucketRow == nil || bucketRow.Copy != CopyNone || !strings.Contains(bucketRow.Where, bucketOfProd) || strings.Contains(bucketRow.CopyNote, "takes no copy") {
		t.Fatalf("the data view: %+v", prot)
	}
	list, err := w.s.List(ctx, w.project)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(list[0].Needs, func(n deployment.NeedSummary) bool { return n.Need == "s3" && n.Name == bucketOfProd }) {
		t.Fatalf("the listing's needs: %+v", list[0].Needs)
	}

	// The export is the deployment's own key reading its bucket after the
	// app has gone: the fake refuses a key the account no longer holds.
	appsAtExport := -1
	w.buckets.atExport = func() { appsAtExport = len(w.api.Snapshot().Apps) }
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down door: %+v %v", res, err)
	}
	op := w.run(res.Operation)
	if !endedAs(op, OperationVerified) || !strings.Contains(op.Detail, "holds its data") || strings.Contains(op.Detail, "Kept") {
		t.Fatalf("take-down %s: %s", op.State, op.Detail)
	}
	if appsAtExport != 0 {
		t.Fatalf("the bucket was copied while %d apps could write", appsAtExport)
	}
	if steps := w.buckets.steps(); !slices.Equal(steps, []string{"export", "import", "verify"}) {
		t.Fatalf("the bucket's copy and check: %v", steps)
	}
	for _, j := range w.buckets.jobs[1:] {
		if j.env["S3_HOST"] != "s3" {
			t.Fatalf("the check filled %s, not a new store", j.env["S3_HOST"])
		}
	}
	if got := w.store.Buckets(); slices.Contains(got, bucketOfProd) {
		t.Fatalf("the bucket stayed though its copy came back: %v", got)
	}
	if keys := w.api.Snapshot().SpacesKeys; len(keys) != 0 {
		t.Fatalf("keys left: %v", keys)
	}
	retained, err := w.s.Retained.ForProject(ctx, w.project)
	if err != nil || slices.ContainsFunc(retained, func(r deployment.RetainedResource) bool {
		name, _ := r.Handle["name"].(string)
		return name == bucketOfProd
	}) {
		t.Fatalf("retained: %+v %v", retained, err)
	}
	finals, err := w.s.FinalCopies(ctx, w.project)
	if err != nil || len(finals) != 1 {
		t.Fatalf("final copies: %+v %v", finals, err)
	}
	set, err := w.s.backups.InProject(ctx, w.project, finals[0].Backup)
	if err != nil || set.GoodCopy("need", "s3") == nil || set.GoodCopy("need", "s3").Bucket != bucketOfProd || set.GoodCopy("need", "s3").Format != "s3-objects" {
		t.Fatalf("the final set: %+v %v", set, err)
	}

	// A deployment started from the final copy has its own new bucket filled
	// from it before its app starts.
	if _, err := w.bind(w.project, "copy", "production", finals[0].Backup); err != nil {
		t.Fatal(err)
	}
	rel, log := w.release("copy")
	if rel.State != deployment.Released {
		t.Fatalf("the start from the copy %s:\n%s", rel.State, log)
	}
	want := map[string]string{"invoices/1.pdf": "application/pdf", "logo.png": "image/png"}
	for key, contentType := range want {
		if got, ok := w.store.Get(bucketOfCopy, key); !ok || got.ContentType != contentType {
			t.Fatalf("the new bucket's %s: %+v %v", key, got, ok)
		}
	}
}

// A take-down whose bucket copy falls short, or does not come back, keeps
// the bucket and its key and the deployment's records: the app is gone,
// the data is where it was, and the operation says so.
func TestATakeDownKeepsABucketWhoseCopyFellShort(t *testing.T) {
	for _, failure := range []string{"export", "check"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  s3: {}\n")
			ctx := context.Background()
			putObjects(w, bucketOfProd)
			w.buckets.failExport, w.buckets.failCheck = failure == "export", failure == "check"
			res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
			if err != nil || res.Operation == nil {
				t.Fatalf("take-down door: %+v %v", res, err)
			}
			op := w.run(res.Operation)
			if endedAs(op, OperationVerified) || !strings.Contains(op.Detail, "data kept") {
				t.Fatalf("take-down %s: %s", op.State, op.Detail)
			}
			if _, ok := w.store.Get(bucketOfProd, "logo.png"); !ok {
				t.Fatal("the bucket's objects went")
			}
			if keys := w.api.Snapshot().SpacesKeys; len(keys) != 1 {
				t.Fatalf("the bucket's key: %v", keys)
			}
			if dep, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil || dep == nil {
				t.Fatalf("the deployment's records went: %v", err)
			}
		})
	}
}

// A set that holds no copy of the bucket's objects (taken without one)
// cannot start a deployment that needs s3: its first release is refused
// before the app is made, rather than starting with an empty bucket.
func TestAStartFromASetWithoutTheBucketIsRefused(t *testing.T) {
	t.Parallel()
	w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n  s3: {}\n")
	ctx := context.Background()
	dep, err := w.s.Deployments.ByName(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := w.clusterOf(dep)["id"].(string)
	// A whole set of the database alone, as a copy taken before buckets were
	// copied holds. Recorded through the set's owner: no copy the forge
	// takes now leaves a held bucket out of a whole set.
	id, err := w.s.backups.Record(ctx, &Backup{ProjectID: w.project, Deployment: "prod", TakenAt: "2026-10-16T10:00:00Z", Trigger: TriggerTakeDown, Path: "ledger/prod/old", Store: StoreBackupStore,
		StoreEndpoint: w.store.API.URL, StoreBucket: "forge-copies", Status: BackupOK, DataClass: "production",
		Items: []BackupItem{{Kind: "need", Name: "postgres", File: "postgres.dump", Bytes: 7, SHA256: "x", Format: "pg-custom", Instance: instance, Database: "app", Copy: CopyDeclared}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bind(w.project, "copy", "production", id); err != nil {
		t.Fatal(err)
	}
	apps := len(w.api.Snapshot().Apps)
	rel, log := w.release("copy")
	if rel.State == deployment.Released || !strings.Contains(log, "holds no copy of the s3 bucket's objects") {
		t.Fatalf("a start from a set without the bucket: %s\n%s", rel.State, log)
	}
	if len(w.api.Snapshot().Apps) != apps {
		t.Fatal("the refused start made an app")
	}
}

// What the driver records for the bucket is its owned list's, not a
// managed need instance: database copies, drills and cluster deletion
// never see it.
func TestABucketIsNoManagedNeedInstance(t *testing.T) {
	t.Parallel()
	w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  s3: {}\n")
	dep, err := w.s.Deployments.ByName(context.Background(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	insts, err := w.s.Needs.ForDeployment(context.Background(), dep.ID)
	if err != nil || len(insts) != 0 {
		t.Fatalf("need instances %+v %v", insts, err)
	}
	owned, err := w.s.Deployments.Owned(context.Background(), dep.ID)
	if err != nil || !slices.ContainsFunc(owned, func(o deploy.Owned) bool { return o.Kind == digitalocean.KindBucket && o.ID == bucketOfProd && o.Data }) {
		t.Fatalf("owned %+v %v", owned, err)
	}
}

// A deployment that needs only s3 on App Platform holds its bucket: the
// data view lists it, a take-down without a final copy (no store here)
// keeps the bucket as a retained resource with why and deletes its key,
// and the target's recorded inventory names the key, and the bucket with
// it.
func TestAnObjectOnlyDeploymentWithNoStoreKeepsItsBucket(t *testing.T) {
	t.Parallel()
	w := newDataWorldWith(t, dataWorldOptions{contract: []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  s3: {}\n")})
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	ctx := context.Background()
	prot, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || len(prot.Items) != 1 || prot.Items[0].Need != "s3" || !strings.Contains(prot.Items[0].Where, bucketOfProd) {
		t.Fatalf("the data view: %+v %v", prot, err)
	}
	recorded, err := w.s.Inventory.Recorded(ctx, w.target)
	if err != nil || !slices.ContainsFunc(recorded, func(r deploy.Resource) bool {
		return r.Kind == digitalocean.KindSpacesKey && strings.Contains(r.Detail, bucketOfProd)
	}) {
		t.Fatalf("the recorded inventory: %+v %v", recorded, err)
	}
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod", WithoutFinalCopy: "prod"})
	if err != nil || res.Removed == nil {
		t.Fatalf("take-down: %+v %v", res, err)
	}
	if got := w.store.Buckets(); !slices.Contains(got, bucketOfProd) || len(w.api.Snapshot().SpacesKeys) != 0 {
		t.Fatalf("after the take-down: buckets %v keys %v", got, w.api.Snapshot().SpacesKeys)
	}
	retained, err := w.s.Retained.ForProject(ctx, w.project)
	if err != nil || !slices.ContainsFunc(retained, func(r deployment.RetainedResource) bool {
		name, _ := r.Handle["name"].(string)
		why, _ := r.Handle["why"].(string)
		return name == bucketOfProd && strings.Contains(why, "no final copy")
	}) {
		t.Fatalf("retained: %+v %v", retained, err)
	}
}

// Integrations are not needs: an App Platform release does not deliver an
// attached integration's values yet, and its record says which, by name,
// rather than leaving the app to find out.
func TestAnAppPlatformReleaseSaysWhichIntegrationsItDoesNotDeliver(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	if err := w.integrations.Attachments.Attach(context.Background(), w.project, integration.CapErrors, "", `{"dsn":"https://pub@errors.test/7"}`, integration.Entered, "owner", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bind(w.project, "prod", "non-production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	rel, log := w.release("prod")
	if rel.State != deployment.Released || !strings.Contains(log, "give an app on a Docker host GLITCHTIP_DSN, SENTRY_DSN") || !strings.Contains(log, "does not deliver them yet") || strings.Contains(log, "pub@errors.test") {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
}

// A restored copy of a deployment that keeps objects fills a bucket of its
// own from the set, as its own key, and checks it; whether it passes or its
// start fails, its take-down removes that bucket and its key, and never
// prod's, which keeps its objects and its key.
func TestARestoredCopyFillsItsOwnBucketAndTakesItDown(t *testing.T) {
	for _, scenario := range []string{"restore test", "a start that fails"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			w := newRestoredWorld(t, []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n  s3: {}\n"))
			putObjects(w.dataWorld, bucketOfProd)
			op, err := w.s.RequestCopy(context.Background(), w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			if got := w.run(op); !endedAs(got, OperationNotVerified) {
				t.Fatalf("copy %s: %s", got.State, got.Detail)
			}
			if scenario == "a start that fails" {
				w.platform.answer["/"] = http.StatusServiceUnavailable
			}
			op, c := w.start(RestoredCopyRequest{Test: true})
			if scenario == "restore test" && !endedAs(op, OperationVerified) {
				t.Fatalf("restore test %s: %s\n%+v", op.State, op.Detail, op.Steps)
			}
			if !c.EndedAt.Valid || c.Open {
				t.Fatalf("the copy stands: %+v", c)
			}
			var filled string
			for _, j := range w.buckets.jobs {
				if j.step == "restore" {
					filled = j.env["S3_BUCKET"]
				}
			}
			if filled == "" || filled == bucketOfProd {
				t.Fatalf("the copy filled %q", filled)
			}
			if slices.Contains(w.store.Buckets(), filled) {
				t.Fatalf("the copy's bucket %s stands after its take-down", filled)
			}
			if _, ok := w.store.Get(bucketOfProd, "logo.png"); !ok {
				t.Fatal("prod's objects went with the copy")
			}
			keys := w.api.Snapshot().SpacesKeys
			if len(keys) != 1 || keys[0]["name"] != "forge-ledger-prod" {
				t.Fatalf("the keys left: %v", keys)
			}
		})
	}
}

// A copy certifies the bucket it was taken from: a set whose s3 copy is of
// another bucket does not cover this deployment's bucket, so a take-down
// does not delete it on that copy's word.
func TestACopyOfAnotherBucketDoesNotCoverThisOne(t *testing.T) {
	t.Parallel()
	items := []backupPlanItem{{Kind: "need", Name: "s3", Bucket: bucketOfProd}}
	other := &Backup{Status: BackupOK, Items: []BackupItem{{Kind: "need", Name: "s3", Bucket: "forge-ledger-old-a1a1a1a1", File: "s3.tar"}}}
	if why := other.uncoveredManaged(items); !strings.Contains(why, bucketOfProd) {
		t.Fatalf("a copy of another bucket covers this one: %q", why)
	}
	own := &Backup{Status: BackupOK, Items: []BackupItem{{Kind: "need", Name: "s3", Bucket: bucketOfProd, File: "s3.tar"}}}
	if why := own.uncoveredManaged(items); why != "" {
		t.Fatalf("its own copy: %q", why)
	}
}

// A take-down of a deployment that keeps only objects asks again before it
// deletes the bucket, as before a cluster: authority withdrawn, or a stop
// asked, while the bucket's copy is checked leaves the bucket, its objects,
// its key and the deployment's records.
func TestABucketsTakeDownAsksAgainBeforeDeletingIt(t *testing.T) {
	for _, when := range []string{"authority withdrawn", "a stop asked"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  s3: {}\n")
			ctx := context.Background()
			putObjects(w, bucketOfProd)
			res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
			if err != nil || res.Operation == nil {
				t.Fatalf("take-down door: %+v %v", res, err)
			}
			w.buckets.atCheck = func() {
				if when == "authority withdrawn" {
					w.auth.revoke("token:alice")
				} else if _, err := w.ops.Cancel(ctx, res.Operation.ID); err != nil {
					t.Errorf("cancelling the running take-down: %v", err)
				}
			}
			got := w.run(res.Operation)
			if when == "authority withdrawn" {
				withdrawn(t, got)
			} else if got.State != operations.Cancelled || !strings.Contains(got.Detail, "stopped as asked before deleting the bucket") {
				t.Fatalf("a take-down asked to stop: %s %q", got.State, got.Detail)
			}
			if _, ok := w.store.Get(bucketOfProd, "logo.png"); !ok {
				t.Fatal("the bucket's objects went")
			}
			if keys := w.api.Snapshot().SpacesKeys; len(keys) != 1 {
				t.Fatalf("the bucket's key: %v", keys)
			}
			if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err != nil {
				t.Fatalf("the deployment's record went: %v", err)
			}
		})
	}
}

// Each item of a drill keeps its own result: a deployment with a database
// and a bucket shows the database came back into a new database on its
// instance as the app's user, and the bucket into a new store on the copy
// runner as no app user, never the last item's line on both. The limit on
// what is not assessed does not name the bucket the deployment's own s3
// need covers.
//
// Kills: reading the deployment's newest drill row for every item;
// recording a drill without its item.
func TestEachItemSaysWhereItsOwnCopyCameBack(t *testing.T) {
	t.Parallel()
	w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n  s3: {}\n")
	ctx := context.Background()
	putObjects(w, bucketOfProd)
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); !endedAs(got, OperationNotVerified) {
		t.Fatalf("copy %s: %s", got.State, got.Detail)
	}
	drill, err := w.s.RequestDrill(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(drill); !endedAs(got, OperationVerified) {
		t.Fatalf("drill %s: %s", got.State, got.Detail)
	}
	p, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	drills := map[string]*DrillRef{}
	for _, it := range p.Items {
		drills[it.Need] = it.Drill
	}
	pg, s3 := drills["postgres"], drills["s3"]
	if pg == nil || !pg.Passed || pg.User != "app" || pg.Place != "a new database on the deployment's cluster" {
		t.Fatalf("postgres's drill: %+v", pg)
	}
	if s3 == nil || !s3.Passed || s3.User != "" || s3.Place != "a new s3 store on "+w.target.CopyRunner() {
		t.Fatalf("s3's drill: %+v", s3)
	}
	for _, n := range p.Coverage.NotAssessed {
		if strings.Contains(n, "S3 bucket") {
			t.Fatalf("not assessed names the bucket the s3 need covers: %q", n)
		}
	}
}
