package dataprotection

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

const postgresAndBucket = "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n  s3: {}\n"

// A drill records each item of a set alone, the database's before the
// bucket's. When the database's check failed and the bucket's then passed,
// the weekly check is still due: each item is judged by its own last
// check, not by the deployment's newest row. (A check that passes leaves
// it not due: TestADrillIsDueByAppTownsOwnClock.)
//
// Kills: drillDue reading the deployment's newest drill row of any item.
func TestTheWeeklyCheckIsDueWhileAnyItemsLastCheckFailed(t *testing.T) {
	t.Parallel()
	w := heldOnAppPlatform(t, postgresAndBucket)
	ctx := context.Background()
	putObjects(w, bucketOf(w, "prod"))
	dep, err := w.s.Deployments.ByName(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(id+".fake", "app", "ledger: 41")
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); !endedAs(got, OperationNotVerified) {
		t.Fatalf("copy %s: %s", got.State, got.Detail)
	}
	set, err := w.s.newestStoreSet(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Items) != 2 || set.Items[0].Name != "postgres" || set.Items[1].Name != "s3" {
		t.Fatalf("the set's items, in the order a drill fills them: %+v", set.Items)
	}
	w.clusters.importSays = "ERROR: duplicate key value violates unique constraint"
	drill, err := w.s.RequestDrill(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(drill); endedAs(got, OperationVerified) || !strings.Contains(got.Detail, "s3: filled into a new s3 store") {
		t.Fatalf("a drill whose database did not come back and whose bucket did: %s", got.Detail)
	}
	if !w.s.drillDue(ctx, dep, set) {
		t.Fatal("the database's check failed, the bucket's then passed, and the weekly check is not due")
	}
}

// A start from a copy fills only from a copy each item of which its last
// check showed to come back. A final copy whose database did not come back
// (its bucket did) is offered as not checked, and the first release of a
// deployment started from it is refused, naming the database and its last
// check, before anything is made. A person may accept it: the release then
// fills, and its first log line says who accepted which item.
//
// Kills: FinalCopies' "checked" from any passing row of the set; the
// release not asking; the release ignoring a person's acceptance.
func TestAStartFromACopyWhoseDatabaseDidNotComeBackAsksAPerson(t *testing.T) {
	for _, path := range []string{"named", "default"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			w := heldOnAppPlatform(t, postgresAndBucket)
			ctx := context.Background()
			putObjects(w, bucketOf(w, "prod"))
			dep, err := w.s.Deployments.ByName(ctx, w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			id, _ := w.clusterOf(dep)["id"].(string)
			w.clusters.put(id+".fake", "app", "ledger: 41, 42")
			w.clusters.importSays = "ERROR: duplicate key value violates unique constraint"
			res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
			if err != nil || res.Operation == nil {
				t.Fatalf("take-down door: %+v %v", res, err)
			}
			if op := w.run(res.Operation); endedAs(op, OperationVerified) {
				t.Fatalf("a take-down whose database did not come back: %s", op.Detail)
			}
			w.clusters.mu.Lock()
			w.clusters.importSays = ""
			w.clusters.mu.Unlock()
			finals, err := w.s.FinalCopies(ctx, w.project)
			if err != nil || len(finals) != 1 {
				t.Fatalf("final copies: %+v %v", finals, err)
			}
			if finals[0].Verified {
				t.Fatalf("a final copy whose database did not come back is offered as checked: %+v", finals[0])
			}
			copy, err := w.bind(w.project, "copy", "production", finals[0].Backup)
			if err != nil {
				t.Fatal(err)
			}
			request := func(accept bool) (deployment.AcceptedRelease, error) {
				return w.s.RequestRelease(ctx, deployment.DeploySelection{ProjectID: w.project, DeploymentName: "copy", BundleID: w.bundle.ID, AcceptUncheckedCopy: accept})
			}
			if path == "default" {
				selected, err := w.s.SetReleaseDefault(ctx, w.project, copy.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				request = func(accept bool) (deployment.AcceptedRelease, error) {
					return w.s.RequestDefaultRelease(ctx, deployment.DefaultReleaseSelection{ProjectID: w.project, ExpectedRevision: &selected.Revision, AcceptUncheckedCopy: accept})
				}
			}
			apps := len(w.api.Snapshot().Apps)
			before, err := w.s.Releases.ForProject(ctx, w.project, 100)
			if err != nil {
				t.Fatal(err)
			}
			_, err = request(false)
			if err == nil || !strings.Contains(err.Error(), "postgres: its last check (") || !strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "s3:") || !strings.Contains(err.Error(), "--accept-unchecked-copy") {
				t.Fatalf("the first release from a copy whose database did not come back: %v", err)
			}
			if len(w.api.Snapshot().Apps) != apps {
				t.Fatal("the refused release made an app")
			}
			refused, err := w.s.Releases.ForProject(ctx, w.project, 100)
			if err != nil || len(refused) != len(before) {
				t.Fatalf("unchecked refusal recorded a release: %+v %v", refused, err)
			}
			accepted, err := request(true)
			if err != nil {
				t.Fatalf("explicit unchecked-copy acknowledgement was refused: %v", err)
			}
			_ = w.s.ExecuteRelease(ctx, accepted.ID)
			rel, err := w.s.Releases.ByNumber(ctx, w.project, accepted.Number)
			if err != nil {
				t.Fatal(err)
			}
			events, err := w.s.Releases.Events(ctx, accepted.ID)
			if err != nil {
				t.Fatal(err)
			}
			lines := make([]string, 0, len(events))
			for _, event := range events {
				lines = append(lines, event.Text)
			}
			log := strings.Join(lines, "\n")
			if rel.ApprovedBy != "alice" || rel.DeploymentID != copy.ID {
				t.Fatalf("accepted unchecked copy lost attribution: %+v", rel)
			}
			if rel.State != deployment.Released {
				t.Fatalf("the accepted start %s:\n%s", rel.State, log)
			}
			first, _, _ := strings.Cut(log, "\n")
			if !strings.Contains(first, "released by alice accepting a copy not shown to come back whole") || !strings.Contains(first, "postgres: its last check (") {
				t.Fatalf("the release's first log line: %s", first)
			}

			after, err := w.s.FinalCopies(ctx, w.project)
			if err != nil || len(after) != 1 || after[0].Backup != finals[0].Backup || after[0].Verified {
				t.Fatalf("unchecked acceptance rewrote copy verification: %+v %v", after, err)
			}
		})
	}
}
