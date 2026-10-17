package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type landingAdoptionsFunc func(context.Context, database.Queryer, AdoptionPairRequest) (AdoptionPair, error)

func (f landingAdoptionsFunc) RevisionsIn(ctx context.Context, q database.Queryer, in AdoptionPairRequest) (AdoptionPair, error) {
	return f(ctx, q, in)
}
func TestLandingAcceptanceAndAdoptionReceiptUseOneTransaction(t *testing.T) {
	t.Parallel()
	q, work, id := scopeActivityRecord(t, "test", idstest.N(11))
	transitionScopeRun(t, q.db, workflowsql.TransitionRunParams{At: time.Now(), ID: work.ID, NextStatus: "done"})
	recordScopeStep(t, q.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Status: "pass", EvidenceJson: json.RawMessage(`{"approver":"original-acceptor","decisions":[{"approver":"original-acceptor","decision":"approve","alsoApproves":["` + landingMeans + `"]}]}`)})
	landing := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "start", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"landing","stages":[{"name":"start","steps":[{"name":"accept","uses":"approval"}]}]}`), AdoptionID: idstest.N(22)}).ID
	gate := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing, Stage: "start", Name: "accept", Type: "approval", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{"quorum":1,"means":"` + landingMeans + `"}`)})
	if err := workflowsql.New(q.db.SQL()).ReserveLanding(context.Background(), work.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := workflowsql.New(q.db.SQL()).AttachAcceptedLanding(context.Background(), workflowsql.AttachAcceptedLandingParams{LandingRunID: landing, WorkRunID: work.ID}); err != nil || n != 1 {
		t.Fatalf("AttachAcceptedLanding: %d %v", n, err)
	}
	q.db.Pool().SetMaxOpenConns(1)
	unavailable := true
	calls := 0
	failure := errors.New("adoption observation failed")
	owner := approvingLandings(q.db, landingAdoptionsFunc(func(ctx context.Context, db database.Queryer, in AdoptionPairRequest) (AdoptionPair, error) {
		calls++
		if in != (AdoptionPairRequest{work.ProjectID, idstest.N(11), idstest.N(22)}) {
			t.Fatalf("wrong scope %+v", in)
		}
		applied, err := workflowsql.New(db.SQL()).LandingAcceptedWork(ctx, landing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return AdoptionPair{}, err
		}
		if applied != work.ID {
			t.Fatal("port did not see the uncommitted handoff")
		}
		if unavailable {
			return AdoptionPair{}, failure
		}
		return AdoptionPair{From: 3, To: 4}, nil
	}))
	if got, err := owner.apply(context.Background(), work.ID, landing); got != nil || !errors.Is(err, failure) {
		t.Fatalf("required observation: %+v %v", got, err)
	}
	wantEventCount(t, q.db, 0, anyEvent)
	// sql-guarantee: no workflow read enumerates applied landing links store-wide; the total proves the failed observation applied none.
	completionCount(t, q.db, 0, `SELECT COUNT(*) FROM workflow_landings WHERE acceptance_applied`)
	wantEqual(t, "gate evidence", string(wantStepState(t, q.db, gate, StatusPending).Evidence), "{}")
	unavailable = false
	for i := 0; i < 2; i++ {
		got, err := owner.apply(context.Background(), work.ID, landing)
		if err != nil || got == nil || got.ID != landing {
			t.Fatalf("accepted/replayed: %+v %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("replay repeated the observation: %d", calls)
	}
	// The handoff casts no votes; carry judges the acceptance (acceptance_carry.go).
	wantEventCount(t, q.db, 0, ofKind("approval"))
	wantEventCount(t, q.db, 1, kindWith("adoption.moved", "from", "3", "to", "4"))
	wantEventCount(t, q.db, 2, ofKind("landing.autostarted"))
	wantRunState(t, q.db, work.ID, RunDone)
}
