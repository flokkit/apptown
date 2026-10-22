package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestTimingObservationKeepsScopeForBothSelectionModes(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "work", DefinitionJson: json.RawMessage(`{}`)}).ID
	if n, err := workflowsql.New(q.db.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPending.String(), EvidenceJson: json.RawMessage(`{"n":9007199254740993}`), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String()}); err != nil || n != 1 {
		t.Fatalf("store precise timing evidence: %d %v", n, err)
	}
	counted := &countedQueries{Handle: q.db.SQL()}
	rows, err := readTimingsIn(ctx, counted, []ids.ID{run.ProjectID}, TimingWindow{})
	if err != nil || len(rows) != 1 || counted.reads != 1 || rows[0].ID != run.ID || len(rows[0].Steps) != 1 || rows[0].Steps[0].Status != StatusPending || !strings.Contains(string(rows[0].Steps[0].Evidence), "9007199254740993") {
		t.Fatalf("timings %+v reads %d: %v", rows, counted.reads, err)
	}
	rows, err = readTimingsIn(ctx, q.db, []ids.ID{run.ProjectID}, TimingWindow{RunID: &foreign})
	if err != nil || len(rows) != 0 {
		t.Fatalf("explicit Run escaped scope %+v: %v", rows, err)
	}
	rows, err = readTimingsIn(ctx, q.db, nil, TimingWindow{RunID: &run.ID})
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty scope meant all %+v: %v", rows, err)
	}
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	rows, err = s.Timings(ctx, TimingWindow{})
	if err != nil || len(rows) != 1 || rows[0].ID != run.ID {
		t.Fatalf("visible timings %+v: %v", rows, err)
	}
	if _, err := s.Timings(ctx, TimingWindow{RunID: &foreign}); err == nil {
		t.Fatal("foreign timing admitted")
	}
	dbtest.FailNextStatement(t, q.db, "WITH selected AS")
	if _, err := s.Timings(ctx, TimingWindow{}); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("hidden timing failure: %v", err)
	}
	if n, err := workflowsql.New(q.db.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), Status: "not-a-state", EvidenceJson: json.RawMessage(`{"n":9007199254740993}`), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String()}); err != nil || n != 1 {
		t.Fatalf("store invalid timing state: %d %v", n, err)
	}
	if _, err := s.Timings(ctx, TimingWindow{}); err == nil {
		t.Fatal("unknown Step state published")
	}
}
