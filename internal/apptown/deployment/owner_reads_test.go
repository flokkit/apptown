package deployment

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Test observations read through the deployment owner's own records (and the
// audit owner's ledger), not raw SQL: a renamed column breaks compilation
// instead of silently changing what a test checks. Each helper fails the test
// on a read error, so an unreadable record is never mistaken for an absent one.

// storedDeployment is a deployment as the owner reads it back.
func storedDeployment(t *testing.T, db *database.DB, id ids.ID) *Deployment {
	t.Helper()
	d, err := NewDeployments(db).ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("deployment %d: %v", id, err)
	}
	return d
}

// everyDeployment is every deployment on the instance (an unbounded read).
func everyDeployment(t *testing.T, db *database.DB) []ProjectDeployment {
	t.Helper()
	all, err := NewDeployments(db).All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// storedAttempt is one deployment attempt's stored row.
func storedAttempt(t *testing.T, db *database.DB, id ids.ID) deploymentsql.DeploymentAttempt {
	t.Helper()
	a, err := deploymentsql.New(db.SQL()).AttemptByID(context.Background(), id)
	if err != nil {
		t.Fatalf("attempt %d: %v", id, err)
	}
	return a
}

// attemptsIn is every attempt on the instance in the given states; with no
// states it is every attempt in every state the AttemptState codec names.
func attemptsIn(t *testing.T, db *database.DB, states ...AttemptState) []deploymentsql.DeploymentAttempt {
	t.Helper()
	if len(states) == 0 {
		states = []AttemptState{AttemptRunning, AttemptUncertain, AttemptDone, AttemptFailed, AttemptSuperseded}
	}
	var out []deploymentsql.DeploymentAttempt
	for _, s := range states {
		rows, err := deploymentsql.New(db.SQL()).InterruptedAttempts(context.Background(), deploymentsql.InterruptedAttemptsParams{Status: s.String(), Status_2: s.String()})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rows...)
	}
	return out
}

// releaseLimit bounds the project listing; a full page fails the test, so
// a listing shorter than it is the project's every release.
const releaseLimit = 1000

// projectReleases is every release of a project, newest first.
func projectReleases(t *testing.T, db *database.DB, projectID ids.ID) []*Release {
	t.Helper()
	rels, err := NewReleases(db).ForProject(context.Background(), projectID, releaseLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) >= releaseLimit {
		t.Fatalf("project %d has %d releases: the listing may be incomplete", projectID, len(rels))
	}
	return rels
}

// storedRelease is one release of a project as the owner reads it back.
func storedRelease(t *testing.T, db *database.DB, projectID, id ids.ID) *Release {
	t.Helper()
	for _, r := range projectReleases(t, db, projectID) {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("release %d is not in project %d", id, projectID)
	return nil
}

// releaseEvents is a release's log.
func releaseEvents(t *testing.T, db *database.DB, releaseID ids.ID) []ReleaseEvent {
	t.Helper()
	events, err := NewReleases(db).Events(context.Background(), releaseID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// everyReleaseEvent is every release event on the instance: each release's
// whole log over every release. Events reference their release, so an event
// written to any release, not only the one a test watches, is counted.
func everyReleaseEvent(t *testing.T, db *database.DB) []ReleaseEvent {
	t.Helper()
	var out []ReleaseEvent
	for _, r := range everyRelease(t, db) {
		out = append(out, releaseEvents(t, db, r.ID)...)
	}
	return out
}

// receipts is every audit receipt of exactly this kind across the instance
// (every kind when empty), newest first, that also satisfies each filter.
func receipts(t *testing.T, db *database.DB, kind string, filters ...func(audit.Entry) bool) []audit.Entry {
	t.Helper()
	rows, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) >= audit.MaxLimit {
		t.Fatalf("%d %q receipts: the ledger page may be incomplete", len(rows), kind)
	}
	var out []audit.Entry
next:
	for _, r := range rows {
		if kind != "" && r.Kind != kind {
			continue
		}
		for _, keep := range filters {
			if !keep(r) {
				continue next
			}
		}
		out = append(out, r)
	}
	return out
}

func byActor(actor string) func(audit.Entry) bool {
	return func(e audit.Entry) bool { return e.Actor == actor }
}

// wantN fails when a read did not find exactly n records.
func wantN[T any](t *testing.T, what string, got []T, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("%s: %d, want %d", what, len(got), n)
	}
}

// deploymentsWhere counts the instance's deployments that satisfy keep.
func deploymentsWhere(t *testing.T, db *database.DB, keep func(*Deployment) bool) int {
	t.Helper()
	n := 0
	for _, d := range everyDeployment(t, db) {
		if keep(&d.Deployment) {
			n++
		}
	}
	return n
}

// attemptsWhere counts the instance's attempts in the given states (every
// state when none) that satisfy keep.
func attemptsWhere(t *testing.T, db *database.DB, keep func(deploymentsql.DeploymentAttempt) bool, states ...AttemptState) int {
	t.Helper()
	n := 0
	for _, a := range attemptsIn(t, db, states...) {
		if keep(a) {
			n++
		}
	}
	return n
}

// wantCount fails when a count is not the expected one.
func wantCount(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: %d, want %d", what, got, want)
	}
}

// queuedJobs is the jobs owner's listing of AppTown operations, filtered to one
// operation and one state ("" for any). A full page fails the test, so the
// listing is every operation on the queue.
func queuedJobs(t *testing.T, db *database.DB, operation, state string) []jobs.Job {
	t.Helper()
	const limit = 500
	all, err := jobs.List(context.Background(), db, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) >= limit {
		t.Fatalf("%d queued jobs: the listing may be incomplete", len(all))
	}
	var out []jobs.Job
	for _, j := range all {
		if (operation == "" || j.Kind == operation) && (state == "" || j.State == state) {
			out = append(out, j)
		}
	}
	return out
}

// observedOf decodes an attempt's stored observation.
func observedOf(t *testing.T, a deploymentsql.DeploymentAttempt) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(a.ObservedJson, &out); err != nil {
		t.Fatalf("attempt %d observation %s: %v", a.ID, a.ObservedJson, err)
	}
	return out
}

// hasObservedKey reports whether an attempt's stored observation has the key
// at all, whatever its value: a JSON null is still a present key.
func hasObservedKey(t *testing.T, a deploymentsql.DeploymentAttempt, key string) bool {
	t.Helper()
	_, ok := observedOf(t, a)[key]
	return ok
}

// receiptPayload decodes a receipt's payload with its keys exactly as written.
func receiptPayload(t *testing.T, e audit.Entry) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(e.Payload, &out); err != nil {
		t.Fatalf("receipt %d payload %s: %v", e.ID, e.Payload, err)
	}
	return out
}

// everyRetained is every retained resource on the instance.
func everyRetained(t *testing.T, db *database.DB) []RetainedResource {
	t.Helper()
	all, err := NewRetained(db).All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// needInstances is a deployment's recorded need instances, every mode.
func needInstances(t *testing.T, db *database.DB, deploymentID ids.ID) []NeedInstance {
	t.Helper()
	needs, err := NeedStore{db: db}.ForDeployment(context.Background(), deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	return needs
}

// everyRelease is every release on the instance: each project's complete
// listing, over every project the project owner has.
func everyRelease(t *testing.T, db *database.DB) []*Release {
	t.Helper()
	projects, err := project.New(db, nil).All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []*Release
	for _, p := range projects {
		out = append(out, projectReleases(t, db, p.ID)...)
	}
	return out
}

// changeReleases is the releases that carried a change.
func changeReleases(t *testing.T, db *database.DB, projectID, changeID ids.ID) []*Release {
	t.Helper()
	rels, err := NewReleases(db).ForChange(context.Background(), projectID, changeID)
	if err != nil {
		t.Fatal(err)
	}
	return rels
}

// lockReleaseProjectIn takes the project lock release admission takes,
// through the release owner's own lock statement.
func lockReleaseProjectIn(tx *database.Tx, projectID ids.ID) error {
	_, err := releasesql.New(tx.SQL()).LockReleaseProject(context.Background(), projectID)
	return err
}

// releaseRequest is a requested release's request row, through the owner's
// request read (a row lock held only for this autocommitted statement).
func releaseRequest(t *testing.T, db *database.DB, releaseID ids.ID) deploymentsql.ReleaseRequest {
	t.Helper()
	row, err := deploymentsql.New(db.SQL()).RequestedReleaseForUpdate(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("release %d request: %v", releaseID, err)
	}
	return row.ReleaseRequest
}

func byProject(projectID ids.ID) func(audit.Entry) bool {
	return func(e audit.Entry) bool { return e.ProjectID == projectID }
}
