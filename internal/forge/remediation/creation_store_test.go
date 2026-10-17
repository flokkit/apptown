package remediation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
)

func remediationCreationFixture(t *testing.T) (Creations, CreationRequest) {
	t.Helper()
	db := testDB(t)
	alpha := createTestProject(t, db, "alpha", "Alpha", "")
	beta := createTestProject(t, db, "beta", "Beta", "")
	first, second, third := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-fffffffffffd"), idstest.Parse(t, "ffffffff-ffff-7fff-bfff-fffffffffffe"), idstest.Parse(t, "ffffffff-ffff-7fff-bfff-ffffffffffff")
	in := CreationRequest{Scope: "review", Ref: idstest.N(42).String(), Actor: "alice", Concurrency: 3, Findings: conformance.RecordedObservations([]conformance.Verdict{
		{ID: first, ProjectID: alpha, ProjectSlug: "alpha", Block: "hygiene", Version: 2, Check: "secure", SHA: "first-sha", EvaluatorVer: "evaluator-v2", EvidenceHash: "first-evidence", Detail: "First frozen finding"},
		{ID: second, ProjectID: alpha, ProjectSlug: "alpha", Block: "hygiene", Version: 1, Check: "clean", SHA: "older-sha", EvaluatorVer: "evaluator-v1", EvidenceHash: "second-evidence", Detail: "Second frozen finding"},
		{ID: third, ProjectID: beta, ProjectSlug: "beta", Block: "quality", Version: 3, Check: "tested", SHA: "beta-sha", EvaluatorVer: "evaluator-v3", EvidenceHash: "beta-evidence", Detail: "Beta frozen finding"},
	})}
	// sql-guarantee: Keep review ID 42 and its exact historical start stamp; Conformance admission generates its identity and current time.
	completionExec(t, db, `INSERT INTO conformance_reviews(id,number,trigger,actor,started_at,status) VALUES(?,42,'manual','reviewer','2000-01-01T00:00:00Z','done')`, idstest.N(42))
	for i := range in.Findings {
		v := in.Findings[i].Record
		v.ReviewID = idstest.N(42)
		v.State = conformance.DoesNotConform
		v.Evaluator = "sh"
		v.Mode = "posture"
		v.At = "2000-01-01T00:00:00Z"
		// sql-guarantee: Preserve explicit verdict identities above 2^53 and historical provenance; Conformance assessment assigns new identities and timestamps.
		completionExec(t, db, `INSERT INTO conformance_verdicts(id,review_id,project_id,project_slug,sha,block,version,check_name,evaluator,evaluator_ver,mode,state,severity,detail,created_at,excepted_by,evidence_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ReviewID, v.ProjectID, v.ProjectSlug, v.SHA, v.Block, v.Version, v.Check, v.Evaluator, v.EvaluatorVer, v.Mode, v.State, v.Severity, v.Detail, v.At, v.ExceptedBy, v.EvidenceHash)
		in.Findings[i] = conformance.RecordedObservations([]conformance.Verdict{*v})[0]
	}
	return NewCreations(db, nil), in
}

func TestRemediationCreationFreezesTheWholeJob(t *testing.T) {
	t.Parallel()
	store, in := remediationCreationFixture(t)
	job, items, err := store.Create(context.Background(), in)
	if err != nil || job == nil || len(items) != len(in.Findings) {
		t.Fatalf("creation: %+v %+v %v", job, items, err)
	}
	if job.Scope != "review" || job.ScopeRef != idstest.N(42).String() || job.ReviewID != idstest.N(42) || job.Actor != "alice" || job.Mode != "attended" || job.Status != RemRunning || job.Concurrency != 3 {
		t.Fatalf("job: %+v", job)
	}
	for i, item := range items {
		v := in.Findings[i].Record
		rank := i + 1
		if i == 2 {
			rank = 1
		}
		if item.RemediationID != job.ID || item.ProjectID != v.ProjectID || item.ProjectSlug != v.ProjectSlug || item.Block != v.Block || item.Version != v.Version || item.Check != v.Check || item.CheckKey != conformance.CheckKey(v.Block, v.Version, v.Check) || item.Rank != rank || item.State != ItemQueued || item.Attempt != 0 || !item.RunID.IsZero() || !item.ChangeID.IsZero() {
			t.Fatalf("item: %+v", item)
		}
		if item.OriginVerdictID != v.ID || item.OriginSHA != v.SHA || item.OriginEvaluatorVer != v.EvaluatorVer || item.OriginEvidenceHash != v.EvidenceHash || item.OriginDetail != v.Detail {
			t.Fatalf("provenance: %+v from %+v", item, v)
		}
		saved, err := ReadItem(context.Background(), store.db, item.ID)
		if err != nil || !reflect.DeepEqual(saved, item) {
			t.Fatalf("returned item differs from persistence: %+v %+v %v", item, saved, err)
		}
	}
	saved, err := readJob(context.Background(), store.db, job.ID)
	if err != nil || saved.Counts[ItemQueued] != 3 || saved.Counts["total"] != 3 {
		t.Fatalf("summary: %+v %v", saved, err)
	}
	started := receipts(t, store.db, "remediation.started")
	var payload map[string]any
	if len(started) != 1 || json.Unmarshal(started[0].Payload, &payload) != nil {
		t.Fatalf("start receipt: %+v", started)
	}
	if r := started[0]; r.Actor != "alice" || !r.ProjectID.IsZero() || r.Subject != fmt.Sprint(job.ID) || !reflect.DeepEqual(payload, map[string]any{"scope": "review", "ref": idstest.N(42).String(), "items": float64(3)}) {
		t.Fatalf("start receipt: %+v %s", r, r.Payload)
	}
	// sql-guarantee: creation starts no Work anywhere; a global absence that no project-scoped Work read can prove.
	completionCount(t, store.db, 0, `SELECT COUNT(*) FROM runs`)
	// sql-guarantee: as above, no Change in any project.
	completionCount(t, store.db, 0, `SELECT COUNT(*) FROM changes`)
}

func TestRemediationCreationRollsBackMissingRecordsAndCommitFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, table, body string
		deferred          bool
	}{
		{"job omitted", "remediations", "RETURN NULL;", false},
		{"late item omitted", "remediation_items", "IF NEW.project_slug='beta' THEN RETURN NULL; END IF; RETURN NEW;", false},
		{"late item rejected", "remediation_items", "IF NEW.project_slug='beta' THEN RAISE EXCEPTION 'item rejected'; END IF; RETURN NEW;", false},
		{"receipt omitted", "audit_log", "RETURN NULL;", false},
		{"receipt rejected", "audit_log", "RAISE EXCEPTION 'receipt rejected';", false},
		{"commit rejected", "audit_log", "RAISE EXCEPTION 'commit rejected';", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, in := remediationCreationFixture(t)
			if _, _, err := store.Create(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			before := outcomeSnapshot(t, store.db)
			completionExec(t, store.db, `CREATE FUNCTION reject_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+tc.body+` END $$`)
			trigger := `CREATE TRIGGER reject_creation BEFORE INSERT ON ` + tc.table
			if tc.deferred {
				trigger = `CREATE CONSTRAINT TRIGGER reject_creation AFTER INSERT ON ` + tc.table + ` DEFERRABLE INITIALLY DEFERRED`
			}
			completionExec(t, store.db, trigger+` FOR EACH ROW EXECUTE FUNCTION reject_creation()`)
			job, items, err := store.Create(context.Background(), in)
			if err == nil || job != nil || items != nil {
				t.Fatalf("failed creation returned work: %+v %+v %v", job, items, err)
			}
			if after := outcomeSnapshot(t, store.db); after != before {
				t.Fatalf("partial creation or unrelated records changed\nbefore %s\nafter %s", before, after)
			}
			completionExec(t, store.db, `DROP TRIGGER reject_creation ON `+tc.table)
			if _, items, err := store.Create(context.Background(), in); err != nil || len(items) != 3 {
				t.Fatalf("explicit rerun: %+v %v", items, err)
			}
			if counts := recordCounts(t, store.db); counts.Remediations != 2 || counts.Items != 6 {
				t.Fatalf("after rerun: %+v", counts)
			}
			receiptCount(t, store.db, 2, "remediation.started")
		})
	}
}

func TestRemediationCreationRejectsEmptyFindings(t *testing.T) {
	t.Parallel()
	store, in := remediationCreationFixture(t)
	in.Findings = nil
	if job, items, err := store.Create(context.Background(), in); err == nil || job != nil || items != nil {
		t.Fatalf("empty job: %+v %+v %v", job, items, err)
	}
	if counts := recordCounts(t, store.db); counts.Remediations != 0 {
		t.Fatalf("empty job saved: %+v", counts)
	}
	receiptCount(t, store.db, 0, "")
}

func TestRemediationCreationIsInvisibleToWorkersUntilCommit(t *testing.T) {
	t.Parallel()
	store, in := remediationCreationFixture(t)
	// Stop after the first item insert; the old autocommit path exposed it here.
	completionExec(t, store.db, `CREATE FUNCTION pause_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
  IF NEW.rank=2 THEN PERFORM pg_advisory_xact_lock(927184); END IF; RETURN NEW; END $$`)
	completionExec(t, store.db, `CREATE TRIGGER pause_creation BEFORE INSERT ON remediation_items FOR EACH ROW EXECUTE FUNCTION pause_creation()`)
	blocker, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	// sql-guarantee: takes the advisory lock the test trigger waits on; a lock, not a read of any record.
	if _, err := blocker.Exec(`SELECT pg_advisory_xact_lock(927184)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var job *Remediation
	var items []Item
	var createErr error
	go func() { job, items, createErr = store.Create(context.Background(), in); close(done) }()
	defer func() {
		_ = blocker.Rollback()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("creator did not finish after releasing test gate")
		}
	}()
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		var waiting int
		// sql-guarantee: the test gate is a Postgres lock wait; only the server's activity view shows the creator parked on it.
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return true
		}
		select {
		case <-done:
			t.Fatalf("creator missed test gate: %v", createErr)
		default:
		}
		return false
	}) {
		t.Fatal("creator did not reach second item")
	}
	if counts := recordCounts(t, store.db); counts.Remediations != 0 || counts.Items != 0 {
		t.Fatalf("uncommitted creation visible: %+v", counts)
	}
	receiptCount(t, store.db, 0, "")
	scheduler := NewSchedule(store.db, nil)
	queue, err := scheduler.Queue(context.Background())
	if err != nil || len(queue.Jobs) != 0 {
		t.Fatalf("worker saw partial job: %+v %v", queue, err)
	}
	if err := scheduler.CloseFinished(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("creator did not commit")
	}
	if createErr != nil || job == nil || len(items) != 3 {
		t.Fatalf("creation: %+v %+v %v", job, items, createErr)
	}
	queue, err = scheduler.Queue(context.Background())
	if err != nil || len(queue.Jobs) != 1 || len(queue.Jobs[0].Items) != 3 {
		t.Fatalf("worker missed committed work: %+v %v", queue, err)
	}
	if n := jobsWithStatus(t, store.db, RemRunning); n != 1 {
		t.Fatalf("running jobs: %d", n)
	}
	receiptCount(t, store.db, 1, "remediation.started")
}

func TestRemediationCreationRejectsUnassessedInput(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	store := NewCreations(db, nil)
	request := CreationRequest{Scope: "fleet", Actor: "t", Findings: []conformance.Observation{conformance.Unassessed(conformance.CheckRef{ProjectID: idstest.N(7)})}}
	if job, items, err := store.Create(context.Background(), request); err == nil || job != nil || items != nil {
		t.Fatalf("unassessed finding admitted: %+v %+v %v", job, items, err)
	}
	if counts := recordCounts(t, db); counts.Remediations != 0 {
		t.Fatalf("unassessed job saved: %+v", counts)
	}
	receiptCount(t, db, 0, "remediation.started")
}
