package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Window boundaries, historical selection, duplicate ledger rows and transaction
// visibility are Workflow's persistence guarantees, independently seeded here.
func TestWorkflowTimingWindowAndQueryCount(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "timing", Name: "Timing"})
	})
	if err != nil {
		t.Fatal(err)
	}
	born := time.Date(2020, 1, 2, 3, 4, 5, 123456000, time.UTC)
	insertRun := func(name string, at time.Time) ids.ID {
		return insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(at), ProjectID: pid, WorkflowName: name, WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "apply", DefinitionJson: []byte(`{}`), CreatedAt: at}).ID
	}
	old := insertRun("old", born.Add(-time.Second))
	boundary := insertRun("boundary", born)
	first := insertRun("busy", born.Add(time.Second))
	counted := &countedQueries{Handle: db.SQL()}
	check := func(window TimingWindow, want int) []RunTiming {
		t.Helper()
		counted.reads = 0
		runs, err := readTimingsIn(context.Background(), counted, []ids.ID{pid}, window)
		if err != nil || len(runs) != want || counted.reads != 1 {
			t.Fatalf("window %+v: %d runs in %d reads: %v", window, len(runs), counted.reads, err)
		}
		return runs
	}
	check(TimingWindow{Since: born}, 1)
	for i := range 20 {
		rid := insertRun("busy", born.Add(time.Duration(i+2)*time.Second))
		// Repeated rows of one named step must count its run/reworks only once.
		for range 2 {
			// sql-guarantee: Seed duplicate step rows with precise historical start/finish times for timing aggregation; no step mutation accepts finished_at.
			completionExec(t, db, `INSERT INTO steps(id,run_id,stage,name,type,status,started_at,finished_at,evidence_json)
  VALUES ($1,$2,'apply','build','agent/pi','pass',$3,$4,$5)`, ids.New(time.Now()), rid, born.Add(2*time.Second), born.Add(3*time.Second), `{"usage":{"cost":{"total":0.015}}}`)
		}
		for _, payload := range []string{`{"step":"build"}`, `{"step":"build"}`, `{"step":1}`, `{}`} {
			if rows, err := workflowsql.New(db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: rid, At: time.Now(), Kind: "step.rework", PayloadJson: []byte(payload)}); err != nil || rows != 1 {
				t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
			}
		}
		if rows, err := workflowsql.New(db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: rid, At: time.Now(), Kind: "unrelated", PayloadJson: []byte(`{"step":"build"}`)}); err != nil || rows != 1 {
			t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
		}
	}
	insertRun("zeta", born.Add(time.Second))
	insertRun("alpha", born.Add(time.Second))
	runs := check(TimingWindow{Since: born}, 23)
	for _, run := range runs {
		if run.Workflow != "busy" || run.ID == first {
			continue
		}
		if len(run.Steps) != 2 {
			t.Fatalf("duplicate step rows disappeared: %+v", run)
		}
		for _, step := range run.Steps {
			if step.Reworks != 2 || step.Status != StatusPass || step.ID.IsZero() || !step.StartedAt.Time.Equal(born.Add(2*time.Second)) || !step.FinishedAt.Time.Equal(born.Add(3*time.Second)) {
				t.Fatalf("step projection: %+v", step)
			}
		}
	}
	check(TimingWindow{Workflow: "busy", Since: born}, 21)
	check(TimingWindow{Workflow: "missing", Since: born}, 0)
	for _, id := range []ids.ID{old, boundary, first} {
		run := check(TimingWindow{RunID: &id, Since: time.Now(), Workflow: "ignored"}, 1)[0]
		if run.ID != id || len(run.Steps) != 0 {
			t.Fatalf("historical empty run: %+v", run)
		}
	}
	for _, id := range []ids.ID{idstest.N(999999)} {
		check(TimingWindow{RunID: &id}, 0)
	}
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// sql-guarantee: Rename the same immutable workflow identity only inside the transaction to test local projection visibility and isolation.
	if _, err := tx.SQL().ExecContext(context.Background(), `UPDATE runs SET workflow_name='uncommitted' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	inside, err := readTimingsIn(context.Background(), tx, []ids.ID{pid}, TimingWindow{RunID: &first})
	if err != nil || len(inside) != 1 || inside[0].Workflow != "uncommitted" {
		t.Fatalf("transaction: %+v %v", inside, err)
	}
	if check(TimingWindow{RunID: &first}, 1)[0].Workflow != "busy" {
		t.Fatal("uncommitted state escaped")
	}
}

// Projection retains the precision and nullability consumed by the timeline,
// and a name-only rework receipt applies to that name in every stage.
func TestTimingProjectionPreservesNullAndFractionalStamps(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "precise", Name: "Precise"})
	})
	if err != nil {
		t.Fatal(err)
	}
	born := time.Date(2020, 1, 2, 3, 4, 5, 123456000, time.UTC)
	rid := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(born), ProjectID: pid, WorkflowName: "timing", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "apply", DefinitionJson: []byte(`{}`), CreatedAt: born}).ID
	timePtr := func(at time.Time) *time.Time { return &at }
	expected := []struct {
		stage, name, typ string
		start, finish    *time.Time
		reworks          int
	}{
		{"apply", "build", "agent/pi", timePtr(born.Add(250 * time.Millisecond)), timePtr(born.Add(1500 * time.Millisecond)), 1},
		{"review", "build", "agent/review", timePtr(born.Add(time.Second)), timePtr(born.Add(2 * time.Second)), 1},
		{"review", "accept", "approval", nil, timePtr(born.Add(2500 * time.Millisecond)), 0},
		{"review", "pending", "agent/pi", nil, nil, 0},
		{"review", "running", "agent/pi", timePtr(born.Add(3 * time.Second)), nil, 0},
	}
	for _, step := range expected {
		// sql-guarantee: Seed independent nullable and fractional start/finish stamps, including pending finished rows; no step mutation accepts these timestamps.
		completionExec(t, db, `INSERT INTO steps(id,run_id,stage,name,type,started_at,finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, ids.New(time.Now()), rid, step.stage, step.name, step.typ, step.start, step.finish)
	}
	if rows, err := workflowsql.New(db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: rid, At: time.Now(), Kind: "step.rework", PayloadJson: []byte(`{"step":"build"}`)}); err != nil || rows != 1 {
		t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
	}
	got, err := readTimingsIn(context.Background(), db, []ids.ID{pid}, TimingWindow{RunID: &rid})
	if err != nil || len(got) != 1 || !got[0].CreatedAt.Equal(born) || len(got[0].Steps) != len(expected) {
		t.Fatalf("timing: %+v %v", got, err)
	}
	for i, want := range expected {
		step := got[0].Steps[i]
		if step.Stage != want.stage || step.Name != want.name || step.Type != want.typ || step.Status != StatusPending || step.Reworks != want.reworks || step.StartedAt.Valid != (want.start != nil) || step.FinishedAt.Valid != (want.finish != nil) {
			t.Fatalf("step %d: %+v", i, step)
		}
		if want.start != nil && !step.StartedAt.Time.Equal(*want.start) || want.finish != nil && !step.FinishedAt.Time.Equal(*want.finish) {
			t.Fatalf("step %d timestamp precision: %+v", i, step)
		}
	}
}
