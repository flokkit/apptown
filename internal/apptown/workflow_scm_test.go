package apptown

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func mergeRecordForTest(t *testing.T, a *App, runID ids.ID) scm.Record {
	t.Helper()
	record, err := scmFor(a).Read(workflow.SCMInspectionContext(context.Background(), a.DB), runID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func requireMergeRecord(t *testing.T, a *App, runID ids.ID, state scm.State, result string) {
	t.Helper()
	record := mergeRecordForTest(t, a, runID)
	if record.State != state || record.Result != result {
		t.Fatalf("merge record %+v, want %v/%s", record, state, result)
	}
}

type stoppedMergeDriver struct{ expected, operation string }

func (d *stoppedMergeDriver) Prepare(context.Context, scm.Source, string) (scm.Prepared, error) {
	return scm.Prepared{Kind: scm.LocalMerge, Expected: d.expected}, nil
}
func (d *stoppedMergeDriver) Execute(_ context.Context, _ scm.Source, _ scm.Prepared, operation string) (scm.Execution, error) {
	d.operation = operation
	return scm.Execution{}, errors.New("simulated interruption after durable admission")
}
func (d *stoppedMergeDriver) Inspect(context.Context, scm.Source, scm.Record) (scm.Inspection, error) {
	return scm.Inspection{}, errors.New("unexpected inspection")
}
func pendingMergeForTest(t *testing.T, a *App, runID ids.ID, expected string) scm.Record {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	source := mergeSourceForTest(t, a, run)
	commands := mergesFor(a)
	driver := &stoppedMergeDriver{expected: expected}
	commands.driver = func() scm.Driver { return driver }
	result, err := commands.execute(context.Background(), advanceWorkflowMerge(run, source))
	if err == nil || result.Merged || driver.operation == "" {
		t.Fatalf("pending fixture did not admit: %+v %v", result, err)
	}
	return mergeRecordForTest(t, a, runID)
}

type provenMergeDriver struct {
	scm.Driver
	result string
}

func (d provenMergeDriver) Inspect(context.Context, scm.Source, scm.Record) (scm.Inspection, error) {
	return scm.Inspection{Kind: scm.Found, Result: d.result}, nil
}

func TestSCMInspectionRecordsPendingEffectWhileAbandonmentStaysClosed(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	pendingMergeForTest(t, a, source.RunID, "base")
	if _, err := a.Work.Abandon(a.asAppTown(context.Background(), source.ProjectID, "fixture"), workflow.StopWork{RunID: source.RunID, Reason: "stop"}); err == nil {
		t.Fatal("pending merge abandoned")
	}
	service := scmFor(a)
	result, err := service.Inspect(workflow.SCMInspectionContext(context.Background(), a.DB), source.RunID, provenMergeDriver{result: "historical-effect"})
	if err != nil || result.Kind != scm.Found {
		t.Fatalf("old effect: %+v %v", result, err)
	}
	requireMergeRecord(t, a, source.RunID, scm.Recorded, "historical-effect")
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.Status == workflow.RunRunning })
	requireEvents(t, a, 0, eventKind("run.done"))

}
func TestSCMWorkRequiresItsExactMintedClaim(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	service := scmFor(a)
	ctx := context.Background()
	if _, err := service.Read(ctx, source.RunID); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("ambient read authority: %v", err)
	}
	if _, err := service.InspectPending(ctx, provenMergeDriver{}); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("ambient inspection authority: %v", err)
	}
	if _, err := service.Merge(workflow.SCMInspectionContext(ctx, a.DB), source.RunID, &stoppedMergeDriver{}); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("inspection granted new effect: %v", err)
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	call := advanceWorkflowMerge(run, source)
	permission, err := (workflowMergeAdmission{a.DB}).prepare(ctx, call)
	if err != nil || permission.Admitted == nil {
		t.Fatalf("claim admission: %+v %v", permission, err)
	}
	ctx, err = permission.Admitted.Context(ctx, a.cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Merge(ctx, idstest.N(999999), &stoppedMergeDriver{}); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("claim escaped run: %v", err)
	}
	if _, err := service.InspectPending(ctx, provenMergeDriver{}); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("one run granted global inspection: %v", err)
	}
	other := testDB(t)
	foreign := scm.New(other, workflow.NewSCMWork(other, a.cfg.dataDir, workflowLawRules{}), &scm.RepositoryLocks{}, nil)
	if _, err := foreign.InspectPending(workflow.SCMInspectionContext(context.Background(), a.DB), provenMergeDriver{}); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("inspection escaped database: %v", err)
	}
	if _, err := foreign.Read(ctx, source.RunID); !errors.Is(err, scm.ErrCaller) {
		t.Fatalf("claim escaped database: %v", err)
	}
}
