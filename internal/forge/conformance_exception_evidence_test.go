package forge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestGuidanceGateCannotPassWithoutExceptionEvidence(t *testing.T) {
	t.Parallel()
	a, ctx, change := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "check", Uses: "guidance/conformance"}}}}})
	adopt(t, a, change.ProjectID, "exceptions", 1, `[{"name":"clean","evaluator":"sh","sh":"true"}]`)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"guidance/conformance"}
		deps.Runners = a.workDependencies().Runners
		deps.Report = a.workDependencies().Report
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("guidance step: %+v %v", snapshot, err)
	}
	step := snapshot.Steps[0]
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: change.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source.Workspace(a.cfg.dataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, source.Workspace(a.cfg.dataDir)) // a check reads a commit
	calls := 0
	installCheckEffects(t, a, checkOverrides{run: func(ExecRequest) ExecOutcome { calls++; return ExecOutcome{} }})
	dbtest.FailNextStatement(t, a.DB, "FROM dispositions")
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(context.Background(), a.DB, step.ID)
	if err != nil || saved.Status != workflow.StatusError || !strings.Contains(string(saved.Evidence), "reading exceptions") || calls == 0 {
		t.Fatalf("gate did not fail for missing exception evidence: step=%+v calls=%d err=%v", saved, calls, err)
	}
}

// The gate's step evidence carries what other owners judge, readable by
// workflow's one reader: a check that could not run is a check error (the
// acceptance carry holds for it), and a boolean failure on main and the
// change alike is undecided (the reviewer is shown it).
func TestGuidanceGateRecordsCheckErrorsAndUndecidedForOtherOwners(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	tgLocalSh(t, a)
	pid, _, _ := fleetRepo(t, a, "facts", map[string]string{"main.go": "package main // FIXME\n"})
	adopt(t, a, pid, "facts", 1, `[{"name":"broken","evaluator":"sh","sh":"exit 2","severity":"high"},{"name":"no-fixme","evaluator":"sh","sh":"grep -rq FIXME . && exit 1; exit 0","severity":"high"}]`)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "check", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}}}}})
	recordFixtureAdoptions(t, a)
	ctx := a.asEngine(context.Background())
	a.Work.Stop()
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "tidy"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "check"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("gate admission: %+v %v", snapshot, err)
	}
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID)
	if err := mirrorBranchForTest(a.projectRepoPath("facts"), ws, change.Branch); err != nil {
		t.Fatal(err)
	}
	if err := a.Work.ExecuteStep(ctx, snapshot.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(ctx, a.DB, snapshot.Steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := workflow.ReadCheckEvidence(saved.Evidence)
	if err != nil || facts.CheckErrors != 1 || len(facts.Checks) != 2 {
		t.Fatalf("check errors: %+v %v: %s", facts, err, saved.Evidence)
	}
	if len(facts.Undecided) != 1 || !strings.Contains(facts.Undecided[0], "no-fixme") {
		t.Fatalf("undecided: %+v: %s", facts.Undecided, saved.Evidence)
	}
}
