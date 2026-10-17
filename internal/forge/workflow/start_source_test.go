package workflow

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func pinStartFixture(t *testing.T, owner runAdmissions, req StartRequest, def *PreparedRun) ids.ID {
	t.Helper()
	if err := owner.records.db.InTx(context.Background(), func(tx *database.Tx) error {
		return adoption.RecordIn(t.Context(), tx, req.ProjectID, 1, adoption.AdoptionEdit, "fixture", adoption.EmptyReceipt(), "fixture", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	id, err := adoption.CurrentReceiptIDIn(context.Background(), owner.records.db, req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	def.AdoptionID = id
	return id
}
func TestStartSourceOwnsScopeAndAdoptionLineage(t *testing.T) {
	t.Parallel()
	owner, req, def := runAdmissionFixture(t)
	db := owner.records.db
	ctx := context.Background()
	pin := pinStartFixture(t, owner, req, &def)
	source, err := owner.Source(ctx, req)
	if err != nil || source.Change.ID != req.ChangeID || source.AdoptionID != pin || len(source.Superseded) != 0 {
		t.Fatalf("root source: %+v %v", source, err)
	}
	other := workflowTestProject(t, db, "foreign", "Foreign", "")
	foreign := req
	foreign.ProjectID = other
	if _, err := owner.Source(ctx, foreign); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign Change: %v", err)
	}
	parent, err := owner.Create(ctx, req, def)
	if err != nil {
		t.Fatal(err)
	}
	child := req
	child.ParentRunID = parent.ID
	source, err = owner.Source(ctx, child)
	if err != nil || source.AdoptionID != pin {
		t.Fatalf("inherited source: %+v %v", source, err)
	}
	// sql-guarantee: the parent-source failure requires clearing only the immutable run adoption pin; no generated Workflow mutation changes adoption_id.
	completionExec(t, db, `UPDATE runs SET adoption_id=NULL WHERE id=?`, parent.ID)
	if _, err := owner.Source(ctx, child); err == nil || !strings.Contains(err.Error(), "parent run") {
		t.Fatalf("missing parent pin chose current project: %v", err)
	}
	// sql-guarantee: the lineage scenario restores only the immutable parent adoption pin before the next source read; no generated Workflow mutation changes adoption_id.
	completionExec(t, db, `UPDATE runs SET adoption_id=? WHERE id=?`, pin, parent.ID)
	transitionStoredRunForTest(t, db, parent.ID, "", RunDone)
	// sql-guarantee: the source reader needs a minimal immutable target receipt; RecordTargetIn requires a fully validated candidate and no Adoption generated query is reachable from Workflow tests.
	target := completionInsert(t, db, `INSERT INTO project_adoptions(id,project_id,revision,change_id,kind,digest,receipt_json,created_at) VALUES (?,?,0,?,'target','target','{}',now())`, req.ProjectID, req.ChangeID)
	changed, err := workflowsql.New(db.SQL()).AttachUpliftTarget(context.Background(), workflowsql.AttachUpliftTargetParams{TargetAdoptionID: target, TargetCandidateJson: []byte(`{}`), ID: req.ChangeID, ProjectID: req.ProjectID})
	if err != nil || changed != 1 {
		t.Fatalf("attach target adoption: changed=%d %v", changed, err)
	}
	source, err = owner.Source(ctx, req)
	if err != nil || source.AdoptionID != target {
		t.Fatalf("Change target lost: %+v %v", source, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := owner.Source(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("source ignored cancellation: %v", err)
	}
}

func TestStartSourceClassifiesFailedLandingWithoutMutatingIt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"failed landing", "active landing", "working", "accepted landing", "read error"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			owner, req, def := runAdmissionFixture(t)
			db := owner.records.db
			ctx := context.Background()
			pinStartFixture(t, owner, req, &def)
			if scenario != "working" {
				def.Spec.Role = "landing"
			}
			run, err := owner.Create(ctx, req, def)
			if err != nil {
				t.Fatal(err)
			}
			insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "first", Name: "failed", Type: "script", Status: StatusFail.String()})
			if scenario == "active landing" {
				insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "first", Name: "active", Type: "script", Status: StatusPending.String()})
			}
			if scenario == "accepted landing" {
				req.AcceptedRunID = idstest.N(99)
			}
			if scenario == "read error" {
				dbtest.FailNextStatement(t, db, "SELECT r.id,COALESCE(r.definition_json")
			}
			source, err := owner.Source(ctx, req)
			if scenario == "failed landing" {
				if err != nil || len(source.Superseded) != 1 || source.Superseded[0] != (RunRef{ID: run.ID, ProjectID: req.ProjectID}) {
					t.Fatalf("failed landing: %+v %v", source, err)
				}
			} else if err == nil {
				t.Fatalf("admitted %s: %+v", scenario, source)
			}
			wantRunState(t, db, run.ID, RunRunning)
			wantEventCount(t, db, 0, ofKind("run.abandoned"))
		})
	}
}

func TestRunDepthLimitIsOwnedBySourceAndTransactionalAdmission(t *testing.T) {
	t.Parallel()
	owner, req, def := runAdmissionFixture(t)
	ctx := context.Background()
	pinStartFixture(t, owner, req, &def)
	for depth := 1; depth <= MaxCallDepth; depth++ {
		run, err := owner.Create(ctx, req, def)
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		if err := owner.Prepared(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		req.ParentRunID = run.ID
	}
	if _, err := owner.Source(ctx, req); err == nil || !strings.Contains(err.Error(), "call depth cap") {
		t.Fatalf("source depth: %v", err)
	}
	if run, err := owner.Create(ctx, req, def); run != nil || err == nil || !strings.Contains(err.Error(), "call depth cap") {
		t.Fatalf("transactional depth: %+v %v", run, err)
	}
	wantRunCount(t, owner.records.db, MaxCallDepth, nil)
}
