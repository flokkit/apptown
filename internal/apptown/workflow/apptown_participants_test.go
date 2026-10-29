package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// The scoped participants AppTown composes with: run counts per workflow on
// one project, a run's preview steps, a schedule's arming and last firing,
// and an artifact stored as text or bytes as its body demands.
func TestAppTownParticipants(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	p := workflowTestProject(t, db, "p", "P", "")
	q := workflowTestProject(t, db, "q", "Q", "")
	var run ids.ID
	for i, r := range [][2]any{{p, "land"}, {p, "land"}, {p, "quick-change"}, {q, "land"}} {
		run = insertStoredRun(t, db, workflowsql.InsertRunParams{Number: int64(i + 1), ID: ids.New(time.Now()), ProjectID: r[0].(ids.ID), WorkflowName: r[1].(string), WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "x", DefinitionJson: []byte(`{}`)}).ID
	}
	counts, err := RunCountsByWorkflowIn(ctx, db, p)
	if err != nil || counts["land"] != 2 || counts["quick-change"] != 1 || len(counts) != 2 {
		t.Fatalf("counts: %v %v", err, counts)
	}
	for _, typ := range []string{"preview", "script", "preview"} {
		insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run, Name: "boot", Type: typ, Stage: "apply", Status: StatusPending.String()})
	}
	if ids, err := PreviewStepIDsIn(ctx, db, run); err != nil || len(ids) != 2 {
		t.Fatalf("preview steps: %v %v", err, ids)
	}
	if _, found, err := ScheduleLastFiredIn(ctx, db, p, "nightly"); err != nil || found {
		t.Fatalf("never armed: %v %v", err, found)
	}
	at := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	if err := ArmScheduleIn(ctx, db, p, "nightly", at); err != nil {
		t.Fatal(err)
	}
	if last, found, err := ScheduleLastFiredIn(ctx, db, p, "nightly"); err != nil || !found || !last.Time.Equal(at) {
		t.Fatalf("armed: %v %v %v", err, found, last)
	}
	id, err := AddArtifactIn(ctx, db, run, "report", "REPORT.md", "", "all good", time.Now())
	if err != nil || id.IsZero() {
		t.Fatalf("text artifact: %v %d", err, id)
	}
	const binary = "\x89PNG\r\n\x1a\n\x00\x00binary\xff\xfe"
	bid, err := AddArtifactIn(ctx, db, run, "bundle", "app.tar", "", binary, time.Now())
	if err != nil || bid.IsZero() {
		t.Fatalf("bytes artifact: %v %d", err, bid)
	}
	var body string
	var bytes []byte
	// sql-guarantee: which column holds the body (text or bytes) is the storage guarantee; the artifact read merges them.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT body, body_bytes FROM artifacts WHERE id=$1`, bid).Scan(&body, &bytes); err != nil || body != "" || string(bytes) != binary {
		t.Fatalf("bytes go to body_bytes unchanged: %v %q %q", err, body, bytes)
	}
	// sql-guarantee: which column holds the body (text or bytes) is the storage guarantee; the artifact read merges them.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT body, body_bytes FROM artifacts WHERE id=$1`, id).Scan(&body, &bytes); err != nil || body != "all good" || bytes != nil {
		t.Fatalf("text goes to body: %v %q", err, body)
	}
}
