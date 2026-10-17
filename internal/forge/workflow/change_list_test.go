package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func TestChangeListPreservesScopeProjectionAndActivity(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := workflowTestProject(t, db, "completion", "Completion", "")
	change := admissionTestChange(t, db, project, "A change")
	historical := time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC)
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(historical), ProjectID: project, ChangeID: change, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`), UpdatedAt: historical}).ID
	transitionScopeChange(t, db, workflowsql.TransitionChangeParams{At: time.Now(), ID: change, RunID: run})
	other := workflowTestProject(t, db, "other", "Other", "")
	createScopeChange(t, db, ChangeDraft{ProjectID: other, Type: "feature", Title: "Other project"})
	if n, err := workflowsql.New(db.SQL()).EditChangeDescription(context.Background(), workflowsql.EditChangeDescriptionParams{ID: change, ProjectID: project, Body: sql.NullString{String: "large body", Valid: true}}); err != nil || n != 1 {
		t.Fatalf("store projected body: %d %v", n, err)
	}
	scopeChangeBase(t, db, change, "historical-base")
	// sql-guarantee: The list projection compares timezone-normalized created_at and historical closed_at against the run timestamp; InsertChange cannot take these clocks and TransitionChange stamps database now().
	completionExec(t, db, `UPDATE changes SET created_at='2020-01-02T03:04:05.123456+11:00',closed_at='2020-01-03T00:00:00Z' WHERE id=?`, change)
	counted := &countedQueries{Handle: db.SQL()}
	rows, err := readChangeList(context.Background(), counted, project, ChangeList{})
	if err != nil || len(rows) != 1 || counted.reads != 1 {
		t.Fatalf("list: %d reads %d rows %v", counted.reads, len(rows), err)
	}
	row := rows[0]
	if row.ID != change || row.Body != "large body" || row.BaseSHA != "historical-base" || row.CreatedAt != "2020-01-01T16:04:05.123456Z" || row.UpdatedAt.String() != "2020-01-04T00:00:00Z" || row.RunStatus.State != RunRunning {
		t.Fatalf("projection: %+v", row)
	}
	rows, err = readChangeList(context.Background(), db, other, ChangeList{})
	if err != nil || len(rows) != 1 || rows[0].RunStatus.State != 0 || rows[0].WorkflowName != "" {
		t.Fatalf("foreign project without a run: %+v %v", rows, err)
	}
	for range 205 {
		createScopeChange(t, db, ChangeDraft{ProjectID: project, Type: "feature", Title: "later"})
	}
	rows, err = readChangeList(context.Background(), db, project, ChangeList{State: ChangeInProgress, Limit: 200, Summary: true})
	if err != nil || len(rows) != 200 {
		t.Fatalf("bounded Summary: %d %v", len(rows), err)
	}
	for i, r := range rows {
		if r.Body != "" || (i > 0 && rows[i-1].Number <= r.Number) {
			t.Fatalf("body/order: %+v", r)
		}
	}
	transitionScopeChange(t, db, workflowsql.TransitionChangeParams{At: time.Now(), ID: change, State: "shipped"})
	rows, err = readChangeList(context.Background(), db, project, ChangeList{State: ChangeShipped, Summary: true})
	if err != nil || len(rows) != 1 || rows[0].ID != change || rows[0].Body != "" {
		t.Fatalf("state/summary filter: %+v %v", rows, err)
	}
	for _, state := range []ChangeState{99, 255} {
		if rows, err := readChangeList(context.Background(), db, project, ChangeList{State: state}); rows != nil || err == nil {
			t.Fatalf("invalid filter accepted: %+v %v", rows, err)
		}
	}
	dbtest.FailNextStatement(t, db, "FROM changes wi LEFT JOIN runs r")
	rows, err = readChangeList(context.Background(), db, project, ChangeList{})
	if err == nil || rows != nil {
		t.Fatalf("read failure became list: %+v %v", rows, err)
	}
}

func createScopeChange(t *testing.T, db *database.DB, in ChangeDraft) ids.ID {
	t.Helper()
	change, err := NewCreator(db, nil).Create(context.Background(), in, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return change.ID
}
