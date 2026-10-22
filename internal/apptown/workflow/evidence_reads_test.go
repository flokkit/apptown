package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestWorkflowEvidenceViewsKeepOrderLimitsAndExactBodies(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	for sequence := 1; sequence <= 205; sequence++ {
		insertRunEventForTest(t, q.db, run.ID, "event", []byte(fmt.Sprintf(`{"number":9007199254740993,"sequence":%d}`, sequence)))
	}
	insertRunEventForTest(t, q.db, run.ID, "guidance.pinned", []byte(`{"number":9007199254740993}`))
	text := "x" + strings.Repeat("🌙", 6000)
	artifact := insertTextArtifactForTest(t, q.db, run.ID, "report", "unicode", "", text, time.Time{})
	binary := []byte{0, 1, 255, 128, 42}
	binaryID := insertBinaryArtifactForTest(t, q.db, run.ID, "file", "historical", "", binary, time.Time{})
	for _, view := range []ReadView{DetailView, TraceView} {
		counted := &countedQueries{Handle: q.db.SQL()}
		snapshot, err := readWorkflowSnapshot(context.Background(), counted, run.ID, view, nil, Placement{}, ids.Nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Run, steps, events, artifacts. The detail view also judges this
		// run's activity, and the run waits at its gate, so one more read
		// fetches the stage's checks receipt for the gate's verdict.
		want := 4
		if view == DetailView {
			want = 5
		}
		if counted.reads != want {
			t.Fatalf("ordinary view made %d reads; activity must reuse its steps", counted.reads)
		}
		wantEvents := 206
		if view == DetailView {
			wantEvents = 200
		}
		if len(snapshot.Events) != wantEvents {
			t.Fatalf("view %d: %d events", view, len(snapshot.Events))
		}
		for i := 1; i < len(snapshot.Events); i++ {
			previous, current := snapshot.Events[i-1].ID, snapshot.Events[i].ID
			if view == DetailView && previous.Compare(current) <= 0 || view == TraceView && previous.Compare(current) >= 0 {
				t.Fatal("event order changed")
			}
		}
		if string(snapshot.PinnedGuidance()) != `{"number":9007199254740993}` {
			t.Fatalf("guidance lost precision: %s", snapshot.PinnedGuidance())
		}
		if len(snapshot.Artifacts) != 2 || snapshot.Artifacts[0].ID != artifact || snapshot.Artifacts[1].ID != binaryID {
			t.Fatalf("artifact order: %+v", snapshot.Artifacts)
		}
		ar := snapshot.Artifacts[0]
		if ar.BodyBytes != int64(len(text)) || !utf8.ValidString(ar.Body) {
			t.Fatalf("text bytes: %+v", ar)
		}
		if view == DetailView {
			if len(ar.Body) > maxArtifactPreview || len(ar.Body) < maxArtifactPreview-3 || !strings.HasPrefix(text, ar.Body) {
				t.Fatal("preview is not a bounded, exact UTF-8 prefix")
			}
		} else if ar.Body != text {
			t.Fatal("trace truncated or altered its full text")
		}
		stored := snapshot.Artifacts[1]
		if !stored.Binary || stored.Body != "" || stored.BodyBytes != int64(len(binary)) {
			t.Fatalf("binary preview lost its identity/size: %+v", stored)
		}
		if view == DetailView && snapshot.State != ActivityWaiting || view == TraceView && snapshot.State != "" {
			t.Fatalf("view activity: %+v", snapshot)
		}
		for _, step := range snapshot.Steps {
			if step.OwnedAttempt() != 0 {
				t.Fatal("read snapshot granted attempt ownership")
			}
		}
	}
	svc := newService(q.db, allowProject(run.ProjectID), Runtime{})
	full, err := svc.Artifact(context.Background(), artifact)
	if err != nil || full.Body != text || full.BodyBytes != int64(len(text)) || full.Binary {
		t.Fatalf("full text changed: %+v %v", full, err)
	}
	full, err = svc.Artifact(context.Background(), binaryID)
	if err != nil || !full.Binary || string(full.Raw) != string(binary) {
		t.Fatalf("binary changed: %+v %v", full, err)
	}
}

func TestWorkflowEvidenceViewsUseOneDatabaseSnapshot(t *testing.T) {
	t.Parallel()
	for _, view := range []ReadView{DetailView, TraceView} {
		t.Run(fmt.Sprint(view), func(t *testing.T) {
			t.Parallel()
			q, run, step := activityFixture(t)
			insertRunEventForTest(t, q.db, run.ID, "before", []byte(`{"number":9007199254740993}`))
			artifact := insertTextArtifactForTest(t, q.db, run.ID, "report", "r", "", "before", time.Time{})
			gate, err := q.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback() }()
			if _, err := gate.Exec(`LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				snapshot *RunSnapshot
				err      error
			}
			done := make(chan result, 1)
			go func() {
				snapshot, err := q.scopedSnapshot(ctx, run.ID, view, run.ProjectID)
				done <- result{snapshot, err}
			}()
			dbtest.WaitForLock(t, q.db, "-- name: ReadArtifacts")
			changed, err := workflowsql.New(gate.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunDone.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision})
			if err != nil || changed != 1 {
				t.Fatalf("change run in competing snapshot: changed=%d %v", changed, err)
			}
			changed, err = workflowsql.New(gate.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: []byte(`{}`), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String()})
			if err != nil || changed != 1 {
				t.Fatalf("change step in competing snapshot: changed=%d %v", changed, err)
			}
			// sql-guarantee: the competing transaction must change the already-read artifact body under the held table lock; artifact storage has no generated update mutation.
			if _, err := gate.Exec(`UPDATE artifacts SET body='after'`); err != nil {
				t.Fatal(err)
			}
			if changed, err := workflowsql.New(gate.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: run.ID, At: time.Now().UTC(), Kind: "after", PayloadJson: []byte(`{}`)}); err != nil || changed != 1 {
				t.Fatalf("insert competing event: changed=%d %v", changed, err)
			}
			if err := gate.Commit(); err != nil {
				t.Fatal(err)
			}
			out := <-done
			if out.err != nil {
				t.Fatal(out.err)
			}
			s := out.snapshot
			if s.Run.Status != RunRunning || len(s.Steps) != 1 || s.Steps[0].ID != step || s.Steps[0].Status != StatusPending || len(s.Events) != 1 || s.Events[0].Kind != "before" || len(s.Artifacts) != 1 || s.Artifacts[0].ID != artifact || s.Artifacts[0].Body != "before" {
				t.Fatalf("torn snapshot: %+v", s)
			}
			if view == DetailView && s.State != ActivityWaiting {
				t.Fatal("activity came from another revision")
			}
			next, err := q.scopedSnapshot(ctx, run.ID, view, run.ProjectID)
			if err != nil || next.Run.Status != RunDone || len(next.Events) != 2 || next.Artifacts[0].Body != "after" {
				t.Fatalf("new snapshot missed completion: %+v %v", next, err)
			}
		})
	}
}

func TestWorkflowEvidenceRequiredReadsReturnNoPartialSnapshot(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{"SET TRANSACTION ISOLATION LEVEL", "-- name: RunByID", "-- name: RunSteps", "-- name: ReadRunEvents", "-- name: ReadArtifacts"} {
		t.Run(statement, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			dbtest.FailNextStatement(t, q.db, statement)
			if snapshot, err := q.scopedSnapshot(context.Background(), run.ID, DetailView, run.ProjectID); snapshot != nil || err == nil || errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("failed %s: %+v %v", statement, snapshot, err)
			}
		})
	}
}

func TestWorkflowEvidenceCancellationReleasesReadSnapshot(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	gate, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := gate.Exec(`LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		snapshot, err := q.scopedSnapshot(ctx, run.ID, DetailView, run.ProjectID)
		if snapshot != nil {
			t.Error("cancelled snapshot returned records")
		}
		done <- err
	}()
	dbtest.WaitForLock(t, q.db, "-- name: ReadArtifacts")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read snapshot ignored cancellation")
	}
}

func TestWorkflowEvidenceMissingAndUnavailableAreDifferent(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	if snapshot, err := q.scopedSnapshot(context.Background(), idstest.N(99999), DetailView, run.ProjectID); snapshot != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing run: %+v %v", snapshot, err)
	}
	if _, err := q.scopedSnapshot(context.Background(), run.ID, 0, run.ProjectID); err == nil {
		t.Fatal("invalid view accepted")
	}
	empty, err := q.scopedSnapshot(context.Background(), run.ID, TraceView, run.ProjectID)
	if err != nil || empty.Events == nil || len(empty.Events) != 0 || empty.Artifacts == nil || len(empty.Artifacts) != 0 {
		t.Fatalf("empty arrays: %+v %v", empty, err)
	}
}

func TestWorkflowEvidenceRejectsAmbiguousArtifactEncoding(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	// sql-guarantee: the decoder law requires one deliberately ambiguous row with both text and bytes populated; typed artifact inserts enforce exactly one representation.
	id := completionInsert(t, q.db, `INSERT INTO artifacts(id,run_id,kind,name,body,body_bytes,created_at) VALUES (?,?,'file','ambiguous','text',?,'2026-10-09T00:00:00Z')`, run.ID, []byte{0, 255})
	if artifact, err := newService(q.db, allowProject(run.ProjectID), Runtime{}).Artifact(context.Background(), id); artifact != nil || err == nil {
		t.Fatalf("ambiguous body was selected: %+v %v", artifact, err)
	}
	if snapshot, err := q.scopedSnapshot(context.Background(), run.ID, DetailView, run.ProjectID); snapshot != nil || err == nil {
		t.Fatalf("ambiguous body entered snapshot: %+v %v", snapshot, err)
	}
	if rows, err := readWorkflowArtifacts(context.Background(), q.db, workflowArtifactQuery{RunID: run.ID}); rows != nil || err == nil {
		t.Fatalf("unspecified projection accepted: %+v %v", rows, err)
	}
}

func TestWorkflowEvidenceRejectsARequiredEventReadFailure(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	// The events are written straight through the query, not
	// insertRunEventForTest, which reads every event back to find the new
	// one's id: a thousand of those is half a gigabyte of reads.
	for sequence := 1; sequence <= 1000; sequence++ {
		if changed, err := workflowsql.New(q.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: run.ID, At: time.Now().UTC(), Kind: "event",
			PayloadJson: []byte(fmt.Sprintf(`{"sequence":%d,"body":"%s"}`, sequence, strings.Repeat("x", 100)))}); err != nil || changed != 1 {
			t.Fatalf("insert run event: changed=%d %v", changed, err)
		}
	}
	stored := storedRunEvents(t, q.db, run.ID)
	last := stored[len(stored)-1].ID
	completionExec(t, q.db, `ALTER TABLE run_events RENAME TO event_stream_records`)
	completionExec(t, q.db, fmt.Sprintf(`CREATE FUNCTION read_event_payload(value jsonb,event_id uuid) RETURNS jsonb LANGUAGE plpgsql AS $$ BEGIN IF event_id='%s' THEN RAISE EXCEPTION 'receipt stream interrupted'; END IF; RETURN value; END $$`, last))
	// sql-guarantee: the view that fails mid-stream on the last event, so a partial event read must surface as an error; DDL, not a read
	completionExec(t, q.db, `CREATE VIEW run_events AS SELECT id,run_id,at,kind,read_event_payload(payload_json,id) AS payload_json FROM event_stream_records`)
	events, err := readWorkflowEvents(context.Background(), q.db, run.ID, TraceView)
	if events != nil || err == nil || !strings.Contains(err.Error(), "receipt stream interrupted") {
		t.Fatalf("partial event stream was returned: %d events, %v", len(events), err)
	}
	snapshot, err := q.scopedSnapshot(context.Background(), run.ID, TraceView, run.ProjectID)
	if snapshot != nil || err == nil || !strings.Contains(err.Error(), "receipt stream interrupted") {
		t.Fatalf("partial event stream was published: %+v %v", snapshot, err)
	}
}

// The page reads a run's check facts as the owner decoded them: a finished
// run's readable evidence with its facts, unreadable evidence (a historical
// model answer with no verdict) as unreadable, never as an empty list, and a
// step without check evidence with no reading at all.
func TestTheSnapshotCarriesTheOwnersReadingOfCheckEvidence(t *testing.T) {
	t.Parallel()
	q, active, _ := activityFixture(t)
	run := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: active.ProjectID, WorkflowName: "done", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "check", DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"check"}]}`)})
	settled := func(name, kind, evidence string) ids.ID {
		id := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "check", Name: name, Type: kind, Status: StatusPending.String()})
		// sql-guarantee: a receipt with an empty verdict is historical; no
		// current operation writes one (CheckEvidence.Outcome refuses it),
		// so the guarded evidence write used by the call fixtures stores it.
		if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(),
			ID: id, ExpectedStatus: StatusPending.String(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(evidence)})); err != nil {
			t.Fatal(err)
		}
		return id
	}
	readable := settled("guidance", "guidance/conformance", `{"checks":[{"block":"b","version":1,"check":"tidy","evaluator":"sh","verdict":"pass"}],"undecided":["a finding"],"reason":"ok"}`)
	unreadable := settled("rubric", "guidance/conformance", `{"checks":[{"block":"b","version":1,"check":"tone","evaluator":"llm","verdict":""}]}`)
	script := settled("build", "script", `{"placement":{"local":true}}`)
	review := settled("review", "agent/review", `{"verdict":"pass"}`)
	snapshot, err := readWorkflowSnapshot(context.Background(), q.db, run.ID, DetailView, nil, Placement{}, ids.Nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[ids.ID]*Step{}
	for _, s := range snapshot.Steps {
		byID[s.ID] = s
	}
	if r := byID[readable].CheckReading; r == nil || r.Unreadable != "" || len(r.Checks) != 1 || r.Checks[0].Verdict != VerdictPass || len(r.Undecided) != 1 {
		t.Errorf("readable evidence: %+v", r)
	}
	if r := byID[unreadable].CheckReading; r == nil || r.Unreadable == "" || r.Checks != nil {
		t.Errorf("unreadable evidence: %+v", r)
	}
	if r := byID[script].CheckReading; r == nil || r.Placement == nil || !r.Placement.Local {
		t.Errorf("a script's placement: %+v", r)
	}
	if r := byID[review].CheckReading; r != nil {
		t.Errorf("a review has no check evidence: %+v", r)
	}
}
