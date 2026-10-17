package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A frozen job over two projects, exactly as a person's start would leave it.
func remediationCreationFixture(t *testing.T, db *database.DB) (remediation.CreationRequest, *conformance.Execution) {
	t.Helper()
	testAccounts(t, db, "reviewer")
	alpha := plainTestProject(t, db, "alpha", "Alpha")
	beta := plainTestProject(t, db, "beta", "Beta")
	first, second, third := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-fffffffffffd"), idstest.Parse(t, "ffffffff-ffff-7fff-bfff-fffffffffffe"), idstest.Parse(t, "ffffffff-ffff-7fff-bfff-ffffffffffff")
	in := remediation.CreationRequest{Scope: "review", Ref: "42", Actor: "alice", Concurrency: 3, Findings: conformance.RecordedObservations([]conformance.Verdict{
		{ID: first, ProjectID: alpha, ProjectSlug: "alpha", Block: "hygiene", Version: 2, Check: "secure", SHA: "first-sha", EvaluatorVer: "evaluator-v2", EvidenceHash: "first-evidence", Detail: "First frozen finding"},
		{ID: second, ProjectID: alpha, ProjectSlug: "alpha", Block: "hygiene", Version: 1, Check: "clean", SHA: "older-sha", EvaluatorVer: "evaluator-v1", EvidenceHash: "second-evidence", Detail: "Second frozen finding"},
		{ID: third, ProjectID: beta, ProjectSlug: "beta", Block: "quality", Version: 3, Check: "tested", SHA: "beta-sha", EvaluatorVer: "evaluator-v3", EvidenceHash: "beta-evidence", Detail: "Beta frozen finding"},
	})}
	review, err := conformance.New(db, testActor{}, remediationFixtureChecks{}, nil).BeginReview(asPerson("reviewer"), conformance.ReviewRequest{Trigger: "manual", Projects: []ids.ID{alpha, beta}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range in.Findings {
		v := in.Findings[i].Record
		v.ReviewID = review.Review().ID
		v.State = conformance.DoesNotConform
		v.Evaluator = "sh"
		v.Mode = "posture"
		v.At = "2000-01-01T00:00:00Z"
		// sql-guarantee: History clearing must preserve frozen references above 2^53 and old verdict timestamps; review execution allocates identities and cannot supply these exact historical IDs.
		completionExec(t, db, `INSERT INTO conformance_verdicts(id,review_id,project_id,project_slug,sha,block,version,check_name,evaluator,evaluator_ver,mode,state,severity,detail,created_at,excepted_by,evidence_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ReviewID, v.ProjectID, v.ProjectSlug, v.SHA, v.Block, v.Version, v.Check, v.Evaluator, v.EvaluatorVer, v.Mode, v.State, v.Severity, v.Detail, v.At, v.ExceptedBy, v.EvidenceHash)
		in.Findings[i] = conformance.RecordedObservations([]conformance.Verdict{*v})[0]
	}
	in.Ref = fmt.Sprint(review.Review().ID)
	return in, review
}

func maintenanceFixtureState(t *testing.T, completed bool) (*App, remediation.Creations, remediation.CreationRequest, ids.ID) {
	t.Helper()
	db := testDB(t)
	in, review := remediationCreationFixture(t, db)
	store := remediation.NewCreations(db, nil)
	a := newApp(t, db)
	testAdministrator(t, a, "operator")
	projectID := in.Findings[0].ProjectID
	disposition, err := a.conformance.CreateDisposition(testConformanceContext("operator"), conformance.ExceptionRequest{ProjectID: projectID, Fingerprint: "fixture", Kind: conformance.AcceptedRisk, Justification: "keep this decision"})
	if err != nil {
		t.Fatal(err)
	}
	exceptions, err := review.Exceptions(context.Background(), projectID)
	if err != nil || len(exceptions) != 1 || exceptions[0].ID != disposition.ID {
		t.Fatalf("review exception snapshot: %+v %v", exceptions, err)
	}
	if _, err := review.Finish(context.Background(), conformance.Completion{Projects: 2, ProjectsDone: 2}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
	job, items, err := store.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	attempts := remediation.NewAttempts(a.DB, a.Work, asPerson("operator"), nil)
	def := remediation.StartDefinition{Title: "Repair frozen finding", Body: "history fixture", Base: "first-sha", Run: workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "history-repair", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work"}}}}}
	if run, err := attempts.Start(context.Background(), items[0], def, "history-worker"); err != nil || run == nil {
		t.Fatalf("start historical attempt: %+v %v", run, err)
	}
	current, err := remediation.ReadItem(context.Background(), a.DB, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := remediation.NewOutcomes(a.DB, nil).Record(context.Background(), current, remediation.Outcome{Kind: "stuck", Reason: "recorded historical attempt"})
	if err != nil || finished == nil {
		t.Fatalf("finish historical attempt: %+v %v", finished, err)
	}
	for _, item := range items {
		if err := attempts.Skip(context.Background(), item.RemediationID, item.ID, "operator", "history fixture complete"); err != nil {
			t.Fatal(err)
		}
	}
	if completed {
		if err := attempts.Close(context.Background(), job.ID); err != nil {
			t.Fatal(err)
		}
	}
	return a, store, in, job.ID
}
func maintenanceFixture(t *testing.T) (*App, remediation.Creations, remediation.CreationRequest) {
	t.Helper()
	a, store, in, _ := maintenanceFixtureState(t, true)
	return a, store, in
}
func historySnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: a refused or failed clear must leave both domains' history byte-identical; only the whole tables, read independently of the clearing code, show that.
	if err := db.QueryRow(`SELECT jsonb_build_object(
 'reviews',(SELECT jsonb_agg(r ORDER BY id) FROM conformance_reviews r),
 'verdicts',(SELECT jsonb_agg(r ORDER BY id) FROM conformance_verdicts r),
 'adjudications',(SELECT jsonb_agg(r ORDER BY id) FROM conformance_adjudications r),
 'snapshots',(SELECT jsonb_agg(r ORDER BY id) FROM conformance_review_exceptions r),
 'fixes',(SELECT jsonb_agg(r ORDER BY id) FROM conformance_fixes r),
 'jobs',(SELECT jsonb_agg(r ORDER BY id) FROM remediations r),
 'items',(SELECT jsonb_agg(r ORDER BY id) FROM remediation_items r),
 'attempts',(SELECT jsonb_agg(r ORDER BY id) FROM remediation_attempts r),
 'exceptions',(SELECT jsonb_agg(r ORDER BY id) FROM dispositions r),
 'audit',(SELECT jsonb_agg(r ORDER BY id) FROM audit_log r))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// historyReviews is every conformance review, failing when the list's cap
// could have cut it short.
func historyReviews(t *testing.T, a *App) []conformance.ReviewSummary {
	t.Helper()
	const cap = 50
	reviews, err := a.conformance.Reviews(testConformanceContext("operator"), cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) >= cap {
		t.Fatal("review list reached its cap")
	}
	return reviews
}

// findingProjects is each project the request's findings name, once.
func findingProjects(in remediation.CreationRequest) []ids.ID {
	var out []ids.ID
	for _, f := range in.Findings {
		if !slices.Contains(out, f.ProjectID) {
			out = append(out, f.ProjectID)
		}
	}
	return out
}

func TestHistoryClearCommitsBothDomainsWithExactCounts(t *testing.T) {
	t.Parallel()
	a, _, in := maintenanceFixture(t)
	result, err := a.Fixes.ClearHistory(testConformanceContext("operator"))
	if err != nil || result == nil {
		t.Fatalf("clear: %+v %v", result, err)
	}
	if result.Reviews != 1 || result.Verdicts != 3 || result.Exceptions != 1 || result.Jobs != 1 || result.Items != 3 || result.Attempts != 1 || result.By != "operator" {
		t.Fatalf("counts: %+v", result)
	}
	// The fixture's dispositions all sit on its findings' projects.
	dispositions := 0
	for _, projectID := range findingProjects(in) {
		kept, err := a.conformance.Dispositions(testConformanceContext("operator"), projectID, false)
		if err != nil {
			t.Fatal(err)
		}
		dispositions += len(kept)
	}
	if dispositions != 1 {
		t.Fatalf("%d dispositions after clearing history, want the one decision kept", dispositions)
	}
	if reviews := historyReviews(t, a); len(reviews) != 0 {
		t.Fatalf("reviews survived: %+v", reviews)
	}
	if jobs, err := a.Fixes.Remediations(context.Background(), 50); err != nil || len(jobs) != 0 {
		t.Fatalf("remediations survived: %+v %v", jobs, err)
	}
	receipts := ledgerEntries(t, a, ids.Nil, "conformance.history-cleared")
	if len(receipts) != 1 || receipts[0].Actor != "operator" {
		t.Fatalf("history-cleared receipts: %+v", receipts)
	}
	// The receipt says exactly what the clear returned, compared as JSON values.
	var got, want any
	if err := json.Unmarshal(receipts[0].Payload, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(mustJSON(struct {
		Removed *remediation.ClearedHistory `json:"removed"`
	}{result})), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt payload %s, want %v", receipts[0].Payload, want)
	}
}
func TestHistoryClearRollsBackMissingDeletesAndAudit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		table, event, body string
		deferred           bool
	}{
		{"audit_log", "INSERT", "RETURN NULL;", false}, {"audit_log", "INSERT", "RAISE EXCEPTION 'commit refused';", true},
		{"conformance_reviews", "DELETE", "RETURN NULL;", false}, {"conformance_verdicts", "DELETE", "RETURN NULL;", false},
		{"remediation_items", "DELETE", "RETURN NULL;", false}, {"remediation_attempts", "DELETE", "RETURN NULL;", false},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			t.Parallel()
			a, _, _ := maintenanceFixture(t)
			before := historySnapshot(t, a.DB)
			completionExec(t, a.DB, `CREATE FUNCTION reject_clear() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+tc.body+` END $$`)
			trigger := "CREATE TRIGGER reject_clear BEFORE " + tc.event + " ON " + tc.table
			if tc.deferred {
				trigger = "CREATE CONSTRAINT TRIGGER reject_clear AFTER " + tc.event + " ON " + tc.table + " DEFERRABLE INITIALLY DEFERRED"
			}
			completionExec(t, a.DB, trigger+` FOR EACH ROW EXECUTE FUNCTION reject_clear()`)
			result, err := a.Fixes.ClearHistory(testConformanceContext("operator"))
			if err == nil || result != nil {
				t.Fatalf("uncommitted clear: %+v %v", result, err)
			}
			if after := historySnapshot(t, a.DB); after != before {
				t.Fatalf("partial history removal\nbefore %s\nafter %s", before, after)
			}
		})
	}
}
func TestHistoryClearRefusesActiveWorkAndLockContention(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"conformance_verdicts", "remediation_items"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			a, _, _ := maintenanceFixture(t)
			before := historySnapshot(t, a.DB)
			holder, err := a.DB.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback() }()
			if _, err := holder.Exec(`LOCK TABLE ` + table + ` IN ROW SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(testConformanceContext("operator"), 2*time.Second)
			defer cancel()
			result, err := a.Fixes.ClearHistory(ctx)
			if !errors.Is(err, conformance.ErrHistoryBusy) || result != nil {
				t.Fatalf("contention should refuse without waiting: %+v %v", result, err)
			}
			if after := historySnapshot(t, a.DB); after != before {
				t.Fatal("refused clear changed history")
			}
		})
	}
	for _, state := range []string{remediation.RemRunning, remediation.RemPaused} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			a, _, _, remediationID := maintenanceFixtureState(t, false)
			if state == remediation.RemPaused {
				if err := remediation.NewAttempts(a.DB, nil, context.Background(), nil).Status(context.Background(), remediationID, []string{remediation.RemRunning}, state, "operator"); err != nil {
					t.Fatal(err)
				}
			}
			before := historySnapshot(t, a.DB)
			if result, err := a.Fixes.ClearHistory(testConformanceContext("operator")); !errors.Is(err, conformance.ErrHistoryBusy) || result != nil {
				t.Fatalf("active job: %+v %v", result, err)
			}
			if historySnapshot(t, a.DB) != before {
				t.Fatal("active work was deleted")
			}
		})
	}
	// A queued review is admitted work waiting for the conformance queue's
	// worker: clearing would delete the request its job reads. Kills: a
	// busy check that counts only running reviews.
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprintf("review claimed %v", claimed), func(t *testing.T) {
			t.Parallel()
			a, _, _ := maintenanceFixture(t)
			ctx := testConformanceContext("operator")
			rev, err := fleetReviewsFor(a).StartFleetReviewWith(ctx, "manual", conformance.ReviewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				if _, err := a.conformance.ClaimReview(ctx, rev.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := historySnapshot(t, a.DB)
			if result, err := a.Fixes.ClearHistory(ctx); !errors.Is(err, conformance.ErrHistoryBusy) || result != nil {
				t.Fatalf("unfinished review: %+v %v", result, err)
			}
			if historySnapshot(t, a.DB) != before {
				t.Fatal("an unfinished review's history was deleted")
			}
			if !claimed {
				if _, err := a.conformance.ClaimReview(ctx, rev.ID); err != nil {
					t.Fatalf("the queued review can still be claimed: %v", err)
				}
			}
		})
	}
}

func holdHistoryReceipt(t *testing.T, db *database.DB) *database.Tx {
	t.Helper()
	completionExec(t, db, `CREATE FUNCTION pause_history_clear() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind='conformance.history-cleared' THEN PERFORM pg_advisory_xact_lock(829175); END IF; RETURN NEW; END $$`)
	completionExec(t, db, `CREATE TRIGGER pause_history_clear BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION pause_history_clear()`)
	gate, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: takes the advisory lock the trigger waits on; it reads nothing.
	if _, err := gate.Exec(`SELECT pg_advisory_xact_lock(829175)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Rollback() })
	return gate
}
func releaseHistoryOnCleanup(t *testing.T, gate *database.Tx, done <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		_ = gate.Rollback()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("history operation did not finish after releasing gate")
		}
	})
}

func TestHistoryClearSerializesNewReviewsAndRejectsStaleRemediationOrigins(t *testing.T) {
	t.Parallel()
	a, store, in := maintenanceFixture(t)
	testAdministrator(t, a, "new-review")
	gate := holdHistoryReceipt(t, a.DB)
	var removed *remediation.ClearedHistory
	var clearErr error
	clearDone := make(chan struct{})
	go func() {
		defer close(clearDone)
		removed, clearErr = a.Fixes.ClearHistory(testConformanceContext("operator"))
	}()
	releaseHistoryOnCleanup(t, gate, clearDone)
	dbtest.WaitForLock(t, a.DB, "INSERT INTO audit_log")
	var review *conformance.Execution
	var reviewErr error
	reviewDone := make(chan struct{})
	go func() {
		defer close(reviewDone)
		review, reviewErr = a.conformance.BeginReview(testConformanceContext("new-review"), conformance.ReviewRequest{Projects: []ids.ID{}})
	}()
	releaseHistoryOnCleanup(t, gate, reviewDone)
	dbtest.WaitForLock(t, a.DB, "INSERT INTO conformance_reviews")
	var job *remediation.Remediation
	var items []remediation.Item
	var startErr error
	startDone := make(chan struct{})
	go func() { defer close(startDone); job, items, startErr = store.Create(context.Background(), in) }()
	releaseHistoryOnCleanup(t, gate, startDone)
	dbtest.WaitForLock(t, a.DB, "INSERT INTO remediations")
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	<-clearDone
	<-reviewDone
	<-startDone
	if clearErr != nil || removed == nil {
		t.Fatalf("clear: %+v %v", removed, clearErr)
	}
	if reviewErr != nil || review == nil {
		t.Fatalf("new review: %+v %v", review, reviewErr)
	}
	if !errors.Is(startErr, conformance.ErrOriginsChanged) || job != nil || items != nil {
		t.Fatalf("stale origins admitted: %+v %+v %v", job, items, startErr)
	}
	// Only the new review is left, and verdicts belong to reviews: none survive.
	reviews := historyReviews(t, a)
	if len(reviews) != 1 || reviews[0].ID != review.Review().ID {
		t.Fatalf("reviews after clear: %+v", reviews)
	}
	if detail, err := a.conformance.Review(testConformanceContext("operator"), reviews[0].ID); err != nil || len(detail.Verdicts) != 0 {
		t.Fatalf("verdicts after clear: %+v %v", detail, err)
	}
	if jobs, err := a.Fixes.Remediations(context.Background(), 50); err != nil || len(jobs) != 0 {
		t.Fatalf("stale remediation admitted: %+v %v", jobs, err)
	}
	if n := len(ledgerEntries(t, a, ids.Nil, "remediation.started")); n != 1 {
		t.Fatalf("%d remediation.started receipts", n)
	}
	if _, err := review.Finish(context.Background(), conformance.Completion{}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryClearCancellationRollsBackBothDomains(t *testing.T) {
	t.Parallel()
	a, _, _ := maintenanceFixture(t)
	before := historySnapshot(t, a.DB)
	gate := holdHistoryReceipt(t, a.DB)
	ctx, cancel := context.WithCancel(testConformanceContext("operator"))
	defer cancel()
	var result *remediation.ClearedHistory
	var clearErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, clearErr = a.Fixes.ClearHistory(ctx)
	}()
	releaseHistoryOnCleanup(t, gate, done)
	dbtest.WaitForLock(t, a.DB, "INSERT INTO audit_log")
	cancel()
	<-done
	if clearErr == nil || result != nil {
		t.Fatalf("canceled clear: %+v %v", result, clearErr)
	}
	if after := historySnapshot(t, a.DB); after != before {
		t.Fatalf("canceled clear changed history\nbefore %s\nafter %s", before, after)
	}
}
