package forge

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func completionInsert(t *testing.T, db *database.DB, q string, args ...any) ids.ID {
	t.Helper()
	// The query's first column and placeholder are the new row's id.
	id := ids.New(time.Now())
	if _, err := db.Exec(q, append([]any{id}, args...)...); err != nil {
		t.Fatal(err)
	}
	return id
}
func completionExec(t *testing.T, db *database.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func completionCount(t *testing.T, db *database.DB, want int, query string, args ...any) {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("%s: got %d, want %d", query, n, want)
	}
}

// complete advances the observed run through the engine. errWorkflowMoved
// reports that the observation did not settle: a stale snapshot, a blocked
// gate, or a run that had already moved.
func completeWorkflowForTest(work *workflow.Service, run *workflow.Run) error {
	out, err := work.AdvanceFrom(asPerson("alice"), run)
	if err != nil {
		return err
	}
	if !out.Advanced || out.RunStatus != workflow.RunDone {
		return workflow.ErrRunMoved
	}
	return nil
}

// workRecords is every run Work holds, each with its steps and every event,
// read through Work's own doors as the engine (workRuns).
func workRecords(t *testing.T, a *App) []*workflow.RunSnapshot {
	t.Helper()
	ctx, runs := workRuns(t, a)
	out := make([]*workflow.RunSnapshot, 0, len(runs))
	for _, run := range runs {
		snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.TraceView)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, snapshot)
	}
	return out
}

// workRuns is every run Work lists, read as the engine, which may read
// every project. The list is uncapped, so counts over it are totals.
func workRuns(t *testing.T, a *App) (context.Context, []workflow.RunSummary) {
	t.Helper()
	ctx := a.asEngine(context.Background())
	runs, err := a.Work.Runs(ctx, workflow.RunQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, runs
}

// requireRuns asserts how many of Work's runs match.
func requireRuns(t *testing.T, a *App, want int, match func(workflow.RunSummary) bool) {
	t.Helper()
	_, runs := workRuns(t, a)
	got := 0
	for _, run := range runs {
		if match == nil || match(run) {
			got++
		}
	}
	if got != want {
		t.Fatalf("matching runs: got %d, want %d", got, want)
	}
}

// requireSteps asserts how many steps across all of Work's runs match.
func requireSteps(t *testing.T, a *App, want int, match func(*workflow.Step) bool) {
	t.Helper()
	ctx, runs := workRuns(t, a)
	got := 0
	for _, run := range runs {
		steps, err := a.Work.RunSteps(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range steps {
			if match == nil || match(step) {
				got++
			}
		}
	}
	if got != want {
		t.Fatalf("matching steps: got %d, want %d", got, want)
	}
}

// workEventCount is how many events across all of Work's runs match.
func workEventCount(t *testing.T, a *App, match func(workflow.Event) bool) int {
	t.Helper()
	got := 0
	for _, record := range workRecords(t, a) {
		for _, event := range record.Events {
			if match == nil || match(event) {
				got++
			}
		}
	}
	return got
}

// requireEvents asserts how many events across all of Work's runs match.
func requireEvents(t *testing.T, a *App, want int, match func(workflow.Event) bool) {
	t.Helper()
	if got := workEventCount(t, a, match); got != want {
		t.Fatalf("matching run events: got %d, want %d", got, want)
	}
}

// eventKind matches events of any of the kinds, optionally on one run only.
func eventKind(kinds ...string) func(workflow.Event) bool {
	return func(e workflow.Event) bool { return slices.Contains(kinds, e.Kind) }
}
func runEventKind(runID ids.ID, kinds ...string) func(workflow.Event) bool {
	return func(e workflow.Event) bool { return e.RunID == runID && slices.Contains(kinds, e.Kind) }
}

// evidenceField is one top-level field of a step's evidence, as text.
func evidenceField(step *workflow.Step, field string) string {
	return exactText(step.Evidence, field)
}

// auditCount is how many ledger receipts have exactly one of the kinds,
// read through the audit owner. Its read is bounded, so a count is a total
// only below the bound.
func auditCount(t *testing.T, db *database.DB, kinds ...string) int {
	t.Helper()
	got := 0
	for _, kind := range kinds {
		got += len(auditEntries(t, db, kind))
	}
	return got
}

// auditEntries is every ledger receipt of exactly one kind, read through
// the audit owner; complete only below its read bound.
func auditEntries(t *testing.T, db *database.DB, kind string) []audit.Entry {
	t.Helper()
	entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == audit.MaxLimit {
		t.Fatalf("%s receipts reach the read bound; the list would not be whole", kind)
	}
	out := entries[:0]
	for _, entry := range entries {
		if entry.Kind == kind {
			out = append(out, entry)
		}
	}
	return out
}
func requireAudits(t *testing.T, db *database.DB, want int, kinds ...string) {
	t.Helper()
	if got := auditCount(t, db, kinds...); got != want {
		t.Fatalf("%v receipts: got %d, want %d", kinds, got, want)
	}
}

// requireJobs asserts how many of the queue's jobs, in any state, match;
// read through the queue's own client, whose list is a total below its bound.
func requireJobs(t *testing.T, a *App, want int, match func(*rivertype.JobRow, jobs.Request) bool) {
	t.Helper()
	const bound = 10_000
	listed, err := a.Jobs().Client.JobList(context.Background(), river.NewJobListParams().First(bound))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Jobs) == bound {
		t.Fatal("jobs reach the read bound; the count would not be a total")
	}
	got := 0
	for _, row := range listed.Jobs {
		request := exactRequest(row.EncodedArgs)
		if match == nil || match(row, request) {
			got++
		}
	}
	if got != want {
		t.Fatalf("matching jobs: got %d, want %d", got, want)
	}
}

// requireReleases asserts how many of a project's releases match, read
// through the release owner, whose list is a total below its bound.
func requireReleases(t *testing.T, a *App, projectID ids.ID, want int, match func(*deployment.Release) bool) {
	t.Helper()
	const bound = 500
	releases, err := a.Deployment.Releases.ForProject(context.Background(), projectID, bound)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) == bound {
		t.Fatal("releases reach the read bound; the count would not be a total")
	}
	got := 0
	for _, release := range releases {
		if match == nil || match(release) {
			got++
		}
	}
	if got != want {
		t.Fatalf("matching releases: got %d, want %d", got, want)
	}
}

// requireChangeReleases asserts how many releases carried a change.
func requireChangeReleases(t *testing.T, a *App, projectID, changeID ids.ID, want int) {
	t.Helper()
	releases, err := a.Deployment.Releases.ForChange(context.Background(), projectID, changeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != want {
		t.Fatalf("releases carrying change %d: got %d, want %d", changeID, len(releases), want)
	}
}

// changeRecord is a Change as Work answers for it to the engine.
func changeRecord(t *testing.T, a *App, id ids.ID) *workflow.Change {
	t.Helper()
	ch, err := a.Work.Change(a.asEngine(context.Background()), id)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// auditTotal is how many receipts the ledger holds, read through the audit
// owner; a total only below its read bound.
func auditTotal(t *testing.T, db *database.DB) int {
	t.Helper()
	entries, err := audit.Recent(context.Background(), db, audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == audit.MaxLimit {
		t.Fatal("receipts reach the read bound; the count would not be a total")
	}
	return len(entries)
}

// queueRecord is every job the queue holds, every field, as one value to
// compare: read through the queue's own client.
func queueRecord(t *testing.T, a *App) string {
	t.Helper()
	const bound = 10_000
	listed, err := a.Jobs().Client.JobList(context.Background(), river.NewJobListParams().First(bound))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Jobs) == bound {
		t.Fatal("jobs reach the read bound; the record would not be whole")
	}
	record, err := json.Marshal(listed.Jobs)
	if err != nil {
		t.Fatal(err)
	}
	return string(record)
}

// payloadField is one top-level field of an event's payload, as text.
func payloadField(e workflow.Event, field string) string {
	return exactText(e.Payload, field)
}

// auditsWhere is how many receipts of one kind match.
func auditsWhere(t *testing.T, db *database.DB, kind string, match func(audit.Entry) bool) int {
	t.Helper()
	got := 0
	for _, entry := range auditEntries(t, db, kind) {
		if match(entry) {
			got++
		}
	}
	return got
}

// runRecord is a run as Work answers for it to the engine.
func runRecord(t *testing.T, a *App, id ids.ID) *workflow.Run {
	t.Helper()
	run, err := a.Work.Run(a.asEngine(context.Background()), id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// fixtureNumbers gives fixture rows a number within their project that no
// other fixture row has: numbers are unique per project, and the counter
// is shared by every test in the package.
var fixtureNumbers atomic.Int64

func fixtureNumber() int64 { return 1000 + fixtureNumbers.Add(1) }
