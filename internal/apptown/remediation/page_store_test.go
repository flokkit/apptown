package remediation

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRemediationPageReadsBatchActivityAndHistory(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	p := createTestProject(t, db, "page", "Page", "")
	other := createTestProject(t, db, "other", "Other", "")
	job := insertStoredRemediation(t, db, remediationsql.InsertRemediationParams{ID: ids.New(time.Now()), Scope: "all", Actor: "alice", Status: "running"})
	paused := insertStoredRemediation(t, db, remediationsql.InsertRemediationParams{ID: ids.New(time.Now()), Scope: "all", Actor: "bob", Status: "paused"})
	insertItem := func(job, project ids.ID, state string, attempt int) ids.ID {
		return insertStoredItem(t, db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: job, ProjectID: project, ProjectSlug: "page", CheckKey: "check", Block: "hygiene", Version: 1, CheckName: "clean", State: state}, attempt, ids.Nil)
	}
	first := insertItem(job, p, ItemWorking, 1)
	counted := &countedQueries{Handle: db.SQL()}
	check := func(want, reads int) []Item {
		t.Helper()
		counted.reads = 0
		items, err := readItems(ctx, counted, &job)
		if err != nil || len(items) != want || counted.reads != reads {
			t.Fatalf("items: %d in %d reads: %v", len(items), counted.reads, err)
		}
		return items
	}
	check(1, 1)
	wantDoing := map[ids.ID]string{first: ""}
	var itemIDs []ids.ID
	for _, tc := range []struct{ state, typ, status, want string }{
		{ItemWorking, "agent/pi", "running", "selected (agent)"},
		{ItemAccepting, "agent/review", "needs_human", "selected (agent, queued)"},
		{ItemLanding, "approval", "pending", "selected (needs a person)"},
		{ItemWorking, "preview", "pending", "selected"},
		{ItemVerifying, "agent/pi", "running", ""},
		{ItemQueued, "agent/pi", "running", ""},
		{ItemStuck, "agent/pi", "running", ""},
		{ItemDone, "agent/pi", "running", ""},
		{ItemSkipped, "agent/pi", "running", ""},
	} {
		steps := []schema.Step{{Name: "finished", Uses: "script"}}
		if tc.status == "running" {
			steps = append(steps, schema.Step{Name: "waiting", Uses: "approval"})
		}
		steps = append(steps, schema.Step{Name: "selected", Uses: tc.typ}, schema.Step{Name: "later", Uses: tc.typ})
		state := map[string]workflow.StepState{"running": workflow.StatusRunning, "needs_human": workflow.StatusNeedsHuman, "pending": workflow.StatusPending}[tc.status]
		run := admitPageRun(t, db, p, "fix", steps, map[string]workflow.StepState{"finished": workflow.StatusPass, "selected": state, "later": state})
		id := insertStoredItem(t, db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: job, ProjectID: p, ProjectSlug: "page", CheckKey: "check", Block: "hygiene", Version: 1, CheckName: "clean", State: tc.state}, 2, run)
		itemIDs = append(itemIDs, id)
		wantDoing[id] = tc.want
		if tc.status == "running" {
			// sql-guarantee: Keep an earlier-ID manual approval in legacy needs_human state to prove running work outranks it; manual approval admission remains pending and cannot claim work.
			completionExec(t, db, `UPDATE steps SET status='needs_human' WHERE run_id=? AND name='waiting'`, run)
		}
		// sql-guarantee: Project historical attempt ordering and fractional started_at values alongside fixed costs; InsertAttempt sets no outcome, reason or cost.
		completionExec(t, db, `INSERT INTO remediation_attempts(id,item_id,n,change_id,run_id,base_sha,outcome,reason,cost_micros,started_at)
  VALUES (?,?,2,'00000000-0016-7000-8000-000000000000',?,'second-sha','landed','second',200,'2020-01-02T03:04:05.123456+11:00'),
   (?,?,1,'00000000-000b-7000-8000-000000000000',?,'first-sha','stuck','first',100,'2020-01-01T00:00:00Z')`, ids.New(time.Now()), id, run, ids.New(time.Now()), id, run)
	}
	// A stale/foreign run reference cannot show another project's activity.
	foreign := insertItem(job, p, ItemWorking, 0)
	run := admitPageRun(t, db, other, "private", []schema.Step{{Name: "private activity", Uses: "agent/pi"}}, map[string]workflow.StepState{"private activity": workflow.StatusRunning})
	// sql-guarantee: Bind a foreign run while attempt stays zero so activity and history both remain hidden; StartItem would increment attempt and remove the unattempted-history guarantee.
	completionExec(t, db, `UPDATE remediation_items SET run_id=? WHERE id=?`, run, foreign)
	wantDoing[foreign] = ""
	// History left behind on an unattempted item remains omitted from this view.
	if err := queries(db).InsertAttempt(ctx, remediationsql.InsertAttemptParams{ID: ids.New(time.Now()), StartedAt: time.Now(), ItemID: foreign, N: 1, ChangeID: idstest.N(1), RunID: idstest.N(1)}); err != nil {
		t.Fatal(err)
	}
	outside := insertItem(paused, p, ItemQueued, 1)
	if err := queries(db).InsertAttempt(ctx, remediationsql.InsertAttemptParams{ID: ids.New(time.Now()), StartedAt: time.Now(), ItemID: outside, N: 1, ChangeID: idstest.N(1), RunID: idstest.N(1)}); err != nil {
		t.Fatal(err)
	}
	items := check(11, 2)
	for i, it := range items {
		if it.Doing != wantDoing[it.ID] || (i > 0 && items[i-1].ID.Compare(it.ID) >= 0) {
			t.Fatalf("activity/order: %+v", items)
		}
	}
	// The page names each item's run by its number within the item's own
	// project; another project's run is not named at all.
	for _, it := range items {
		want := int64(0)
		if slices.Contains(itemIDs, it.ID) {
			run, err := workflow.ReadRunIn(ctx, db, it.RunID)
			if err != nil || run.Number <= 0 {
				t.Fatalf("reading item run: %+v %v", run, err)
			}
			want = run.Number
		}
		if it.RunNumber != want || it.ChangeNumber != 0 {
			t.Fatalf("item %s names run %d change %d, want run %d and no change", it.ID, it.RunNumber, it.ChangeNumber, want)
		}
	}
	// Grow the page further without adding queries.
	for range 20 {
		id := insertItem(job, p, ItemQueued, 0)
		wantDoing[id] = ""
	}
	check(31, 2)
	counted.reads = 0
	histories, err := readAttempts(ctx, counted, job)
	if err != nil || counted.reads != 1 || len(histories) != 10 {
		t.Fatalf("histories: %d in %d reads: %v", len(histories), counted.reads, err)
	}
	if history, ok := histories[first]; !ok || history != nil {
		t.Fatalf("missing history entry: %+v", histories)
	}
	if _, ok := histories[foreign]; ok {
		t.Fatal("unattempted history included")
	}
	if _, ok := histories[outside]; ok {
		t.Fatal("other job history included")
	}
	for _, id := range itemIDs {
		history := histories[id]
		if len(history) != 2 || history[0].N != 1 || history[0].BaseSHA != "first-sha" || history[0].CostMicros != 100 || history[1].N != 2 || history[1].StartedAt != "2020-01-01T16:04:05.123456Z" {
			t.Fatalf("history: %+v", history)
		}
	}
	counted.reads = 0
	occupancy, err := readOccupancy(ctx, counted)
	if err != nil || counted.reads != 3 || occupancy.Busy != 7 || len(occupancy.Holding) != 7 || occupancy.Queued != 21 || occupancy.Workers != 2 {
		t.Fatalf("occupancy: %+v, reads=%d %v", occupancy, counted.reads, err)
	}
	for _, it := range occupancy.Holding {
		if it.Doing != wantDoing[it.ID] {
			t.Fatalf("occupancy activity: %+v", it)
		}
	}
	for _, workers := range []int{0, 5, -1} {
		if err := queries(db).UpsertPool(ctx, remediationsql.UpsertPoolParams{Kind: WorkerKindRemediation, Count: int64(workers), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		got, err := readOccupancy(ctx, db)
		want := workers
		if want < 0 {
			want = 2
		}
		if err != nil || got.Workers != want {
			t.Fatalf("pool: %+v %v", got, err)
		}
	}
	// Project and rank precede ID in a job; occupancy follows update time.
	early := insertStoredItem(t, db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: job, ProjectID: other, ProjectSlug: "aaa", CheckKey: "check", Block: "hygiene", Version: 1, CheckName: "clean", State: ItemDone, Rank: 99}, 0, ids.Nil)
	// sql-guarantee: Change ordering only after verifying initial ID order, using a precise historical update stamp; no item mutation changes rank or accepts updated_at.
	completionExec(t, db, `UPDATE remediation_items SET rank=-1,updated_at='2000-01-01' WHERE id=?`, foreign)
	ordered := check(32, 2)
	if ordered[0].ID != early || ordered[1].ID != foreign {
		t.Fatalf("project/rank ordering: %+v", ordered)
	}
	held, err := readItems(ctx, db, nil)
	if err != nil || held[0].ID != foreign {
		t.Fatalf("holding order: %+v %v", held, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	const exact int64 = 9007199254740993 // a cost above 2^53
	change := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-ffffffffffff")
	// sql-guarantee: Change an existing attempt identity and exact cost above 2^53 only inside the read transaction; no mutation edits its immutable Change binding.
	if _, err := tx.Exec(`UPDATE remediation_attempts SET cost_micros=?,change_id=?,reason='uncommitted' WHERE item_id=? AND n=1`, exact, change, itemIDs[0]); err != nil {
		t.Fatal(err)
	}
	inside, err := readAttempts(ctx, tx, job)
	if err != nil || inside[itemIDs[0]][0].CostMicros != exact || inside[itemIDs[0]][0].ChangeID != change || inside[itemIDs[0]][0].Reason != "uncommitted" {
		t.Fatalf("transaction/exact values: %+v %v", inside, err)
	}
	after, err := readAttempts(ctx, db, job)
	if err != nil || !reflect.DeepEqual(after, histories) {
		t.Fatalf("transaction escaped: %+v %v", after, err)
	}
}

func TestRemediationPageEmptyResponsesAndFailures(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	fixes := readOnly(t, db)
	ctx := context.Background()
	job := insertStoredRemediation(t, db, remediationsql.InsertRemediationParams{ID: ids.New(time.Now()), Scope: "all", Actor: "alice", Status: "running"})
	items, err := fixes.Items(ctx, &job)
	attempts, aerr := fixes.Attempts(ctx, job)
	occupancy, oerr := fixes.Occupancy(ctx)
	if err != nil || aerr != nil || oerr != nil || items == nil || len(items) != 0 || attempts == nil || occupancy.Holding == nil {
		t.Fatalf("empty detail: %+v %+v %+v (%v %v %v)", items, attempts, occupancy, err, aerr, oerr)
	}
	// sql-guarantee: statement fragments for failNextStatement, not reads
	for _, fragment := range []string{"FROM remediation_items i\nLEFT JOIN changes", "COALESCE((SELECT p.count FROM worker_pools"} {
		dbtest.FailNextStatement(t, db, fragment)
		if _, err := fixes.Occupancy(ctx); err == nil {
			t.Fatalf("fragment=%s: occupancy failure hidden", fragment)
		}
	}
	dbtest.FailNextStatement(t, db, "FROM remediation_items i LEFT JOIN remediation_attempts")
	if _, err := fixes.Attempts(ctx, job); err == nil {
		t.Fatal("attempt failure hidden")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := readItems(cancelled, db, &job); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("item cancellation: %+v %v", got, err)
	}
	if got, err := readAttempts(cancelled, db, job); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("history cancellation: %+v %v", got, err)
	}
	if got, err := readOccupancy(cancelled, db); got.Holding != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("occupancy cancellation: %+v %v", got, err)
	}
	for _, missing := range []ids.ID{ids.Nil, idstest.N(999999)} {
		items, err := readItems(ctx, db, &missing)
		if err != nil || items == nil || len(items) != 0 {
			t.Fatalf("missing job became occupancy: %+v %v", items, err)
		}
	}
	// Runtime cooldown remains part of the occupancy a person reads.
	fixes.cool.hold("provider cooldown")
	occupancy, err = fixes.Occupancy(ctx)
	if err != nil || occupancy.CooldownWhy != "provider cooldown" || occupancy.CooldownUntil == "" {
		t.Fatalf("cooldown: %+v %v", occupancy, err)
	}
	// The list door says what each job covers as well as how far it has got.
	jobs, err := fixes.Remediations(ctx, 50)
	if err != nil || len(jobs) != 1 || jobs[0].Sentence != "fix all findings · 0 of 0 done" {
		t.Fatalf("list sentence: %+v %v", jobs, err)
	}
	one, err := fixes.Remediation(ctx, job)
	if err != nil || one.Sentence != jobs[0].Sentence {
		t.Fatalf("detail sentence: %+v %v", one, err)
	}
}

func TestRemediationPageReportsWorkObservationFailure(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	fixes := readOnly(t, db)
	project := createTestProject(t, db, "observed", "Observed", "")
	job := insertStoredRemediation(t, db, remediationsql.InsertRemediationParams{ID: ids.New(time.Now()), Scope: "all", Actor: "alice", Status: "running"})
	run := admitPageRun(t, db, project, "fix", []schema.Step{{Name: "work", Uses: "agent/pi"}}, map[string]workflow.StepState{"work": workflow.StatusRunning})
	insertStoredItem(t, db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: job, ProjectID: project, ProjectSlug: "observed", CheckKey: "check", Block: "hygiene", Version: 1, CheckName: "clean", State: ItemWorking}, 1, run)
	dbtest.FailNextStatement(t, db, "-- name: PendingStepsForScopes")
	if _, err := fixes.Items(context.Background(), &job); err == nil {
		t.Fatal("Work failure was hidden as idle")
	}
}
