package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"strings"
	"testing"
	"time"
)

func TestTakeDownOwnsStatusUntilALaterAct(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	if err := jobs.Migrate(t.Context(), w.db.Pool()); err != nil {
		t.Fatal(err)
	}
	auth := &requestAuthority{}
	at := w.at.Add(time.Minute)
	ops := operations.New(w.db, auth, auth, func() time.Time { return at })
	w.s.operations = ops
	ops.Register(operations.Kind{Name: OperationTakeDown, Capability: access.CapDeploymentTeardown, Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
		return operations.Outcome{State: operations.Failed, Detail: "listener removed; service not confirmed removed"}
	}})
	caller, err := auth.Require(t.Context(), access.Permission{})
	if err != nil {
		t.Fatal(err)
	}
	op, err := database.Transact(t.Context(), w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return ops.AdmitIn(t.Context(), tx, operations.Spec{Kind: OperationTakeDown, ProjectID: w.project, Subject: SubjectOf(w.main), Authority: caller.Principal})
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := w.s.Status(t.Context(), w.main.ID)
	if err != nil || status.Word.String() != "taking down" || !strings.Contains(status.Reason, "taking it down (operation") {
		t.Fatalf("live: %+v %v", status, err)
	}
	rows, err := w.s.List(t.Context(), w.project)
	if err != nil || rows[0].Observation.Status.Word.String() != "taking down" {
		t.Fatalf("list: %+v %v", rows, err)
	}
	if _, err := ops.RunHere(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	w.s.readHostContainersForTest(t, w.main.TargetID)
	status, err = w.s.Status(t.Context(), w.main.ID)
	if err != nil || status.Word != progressLabelFailed || !strings.Contains(status.Reason, "take-down did not finish: listener removed") {
		t.Fatalf("failed: %+v %v", status, err)
	}
	// A later applied release clears the failure; observation time alone cannot.
	w.advance(2 * time.Minute)
	w.land(t, w.s, w.main, w.main.ProjectID)
	w.main = w.reread(t)
	status, err = w.s.Status(t.Context(), w.main.ID)
	if err != nil || strings.Contains(status.Reason, "take-down did not finish") {
		t.Fatalf("later deployment: %+v %v", status, err)
	}
	for _, query := range []string{"-- name: LiveOnSubject", "-- name: LatestEndedOfKind"} {
		dbtest.FailNextStatement(t, w.db, query)
		if got, err := w.s.Status(t.Context(), w.main.ID); err == nil {
			t.Fatalf("ledger failure gave a status: %+v", got)
		}
	}
}

func TestECSNamesItsLimitEventsSource(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	target, err := w.s.Targets.Put(t.Context(), &Target{Name: "aws", Kind: KindAWSECS, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	known, words := w.s.limitEventsSource(t.Context(), &Deployment{TargetID: target.ID})
	if known || words != "ECS does not report them to AppTown" {
		t.Fatalf("source: %v %s", known, words)
	}
}
