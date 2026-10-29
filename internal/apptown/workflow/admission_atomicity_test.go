package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestUnverifiedRootCannotApproveOrOverride(t *testing.T) {
	t.Parallel()
	a := stepOverrideAuthorityFixture(t)
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}})
	if _, err := a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteApprove}); !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("unverified root approved: %v", err)
	}
	if _, err := a.Work.Override(ctx, Override{StepID: a.OverrideStepID, Reason: "unverified"}); !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("unverified root overrode: %v", err)
	}
	wantEventCount(t, a.DB, 0, ofKind("approval", "override"))
}

func TestCreateRunRollsBackWhole(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	db := admissions.records.db
	starter := newStarter(db, NewStages(nil, func(string) bool { return false }), nil, startRuntime{
		Source:   func(context.Context, StartRequest) (StartSource, error) { return StartSource{}, nil },
		Resolve:  func(context.Context, StartRequest, StartSource) (PreparedRun, error) { return def, nil },
		Prepare:  func(context.Context, *Run) error { return nil },
		Dispatch: func(context.Context, *Run) error { return nil },
	}, clocked{})
	service := newService(db, authorityFunc(func(context.Context, operationPermission) (string, error) { return "operator", nil }), Runtime{Starter: starter})
	var runsBefore, eventsBefore int
	// sql-guarantee: rollback must leave no run, receipt or step anywhere; no owner read enumerates every run.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM runs`).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: rollback must leave no run event anywhere; no owner read enumerates every run's events store-wide
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM run_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO steps")
	if _, err := service.StartRun(context.Background(), StartChangeRun{ChangeID: req.ChangeID, Workflow: req.WorkflowName}); err == nil {
		t.Fatal("the injected failure must surface")
	}
	var runs, events, steps int
	// sql-guarantee: rollback must leave no run anywhere; no owner read enumerates every run store-wide
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: rollback must leave no run event anywhere; no owner read enumerates every run's events store-wide
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM run_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: rollback must leave no step anywhere, including one with no readable run; no owner read lists steps store-wide
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM steps`).Scan(&steps); err != nil {
		t.Fatal(err)
	}
	if runs != runsBefore || events != eventsBefore || steps != 0 {
		t.Fatalf("nothing partial may survive: runs %d→%d events %d→%d steps %d", runsBefore, runs, eventsBefore, events, steps)
	}
	if _, err := service.StartRun(context.Background(), StartChangeRun{ChangeID: req.ChangeID, Workflow: req.WorkflowName}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestNewChangeRollsBackWhole(t *testing.T) {
	t.Parallel()
	creator, draft := creationFixture(t)
	dbtest.FailNextStatement(t, creator.records.db, "UPDATE changes SET branch")
	if _, err := creator.Create(context.Background(), draft, "operator"); err == nil {
		t.Fatal("the injected failure must surface")
	}
	// sql-guarantee: rollback leaves no change row in any project.
	completionCount(t, creator.records.db, 0, `SELECT COUNT(*) FROM changes`)
	expect(t, auditCount(t, creator.records.db, "", nil) == 0, "rolled-back creation left a receipt")
	// A retry proves the failed update did not leave a reserved Change identity.
	if _, err := creator.Create(context.Background(), draft, "operator"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	// sql-guarantee: the retry writes exactly one change row in any project.
	completionCount(t, creator.records.db, 1, `SELECT COUNT(*) FROM changes`)
}
