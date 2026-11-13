package apptown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
)

func TestGuidanceGateCannotPassWithoutExceptionEvidence(t *testing.T) {
	t.Parallel()
	a, ctx, change := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "check", Uses: "guidance/conformance"}}}}})
	adopt(t, a, change.ProjectID, "exceptions", 1, `[{"appliesTo": ["repository"], "name":"clean","evaluator":"sh","sh":"true"}]`)
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
	if err := os.MkdirAll(requireLocation(t)(source.Workspace(a.cfg.dataDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	// A check reads a commit, and a repository check one with a file.
	if err := os.WriteFile(filepath.Join(requireLocation(t)(source.Workspace(a.cfg.dataDir)), "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, requireLocation(t)(source.Workspace(a.cfg.dataDir)))
	calls := 0
	installCheckEffects(t, a, checkOverrides{run: func(execRequest) execOutcome { calls++; return execOutcome{} }})
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
	adopt(t, a, pid, "facts", 1, `[{"appliesTo": ["repository"], "name":"broken","evaluator":"sh","sh":"exit 2","severity":"high"},{"appliesTo": ["repository"], "name":"no-fixme","evaluator":"sh","sh":"grep -rq FIXME . && exit 1; exit 0","severity":"high"}]`)
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

// An ordinary change with a timed-out required shell check keeps its error
// receipt and offers retry through the enclosing Standards step.
func TestGuidanceTimeoutOffersRetry(t *testing.T) {
	for _, tc := range []struct {
		name, severity string
		termination    execution.Termination
		want           workflow.StepState
	}{
		{"required timeout", "high", execution.TimedOut, workflow.StatusError},
		{"advisory timeout", "low", execution.TimedOut, workflow.StatusPass},
		{"other error", "high", execution.Exited, workflow.StatusPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			pid, _, _ := fleetRepo(t, a, "timeout", map[string]string{"README.md": "A small app\n"})
			adopt(t, a, pid, "timeout", 1, `[{"appliesTo":["repository"],"name":"slow-rule","evaluator":"sh","sh":"true","severity":"`+tc.severity+`"}]`)
			defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "check", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "standards", Uses: "guidance/conformance"}}}}}})
			recordFixtureAdoptions(t, a)
			calls := 0
			installCheckEffects(t, a, checkOverrides{run: func(execRequest) execOutcome { calls++; return execOutcome{Termination: tc.termination, ExitCode: 2} }})
			ctx := a.asEngine(t.Context())
			change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "Tidy the app"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "check"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			before, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
			if err != nil || len(before.Steps) != 1 {
				t.Fatalf("admission: %+v %v", before, err)
			}
			ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID)
			if err := mirrorBranchForTest(a.projectRepoPath("timeout"), ws, change.Branch); err != nil {
				t.Fatal(err)
			}
			if err := a.Work.ExecuteStep(ctx, before.Steps[0].ID); err != nil {
				t.Fatal(err)
			}
			after, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			step := after.Steps[0]
			facts, err := workflow.ReadCheckEvidence(step.Evidence)
			if err != nil || calls == 0 || step.Status != tc.want || facts.CheckErrors != 1 || len(facts.Checks) != 1 || facts.Checks[0].Verdict != workflow.VerdictError {
				t.Fatalf("timeout result: %+v facts=%+v calls=%d err=%v evidence=%s", step, facts, calls, err, step.Evidence)
			}
			summary, err := a.Work.ChangeChecks(ctx, after)
			if err != nil || summary.Tone == "ok" {
				t.Fatalf("unknown result passed: %+v %v", summary, err)
			}
			retry := false
			for _, act := range after.Next {
				retry = retry || string(act.Act) == "retry" && act.StepID == step.ID
			}
			if retry != (tc.want == workflow.StatusError) {
				t.Fatalf("wrong recovery: %+v", after.Next)
			}
			if tc.want == workflow.StatusError && (!strings.Contains(string(step.Evidence), "slow-rule timed out") || !strings.Contains(string(step.Evidence), "run this step again")) {
				t.Fatalf("missing recovery reason: %s", step.Evidence)
			}
		})
	}
}
