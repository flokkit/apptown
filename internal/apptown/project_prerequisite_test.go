package apptown

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/telemetry/otlpfake"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// plainTestProject creates only the Project record. These tests deliberately
// assemble Adoption, repositories, and other participants independently.
func plainTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: nextFixtureProject(t, db), Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A required project artifact that doesn't exist yet fails the agent step
// before anything is dispatched — no wasted run building from a
// "(no artifact...)" placeholder (complaint #8).
func TestAgentRequiresArtifactPreflight(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	a := testApp(t, Config{OTLPEndpoint: receiver.Endpoint()})
	t.Cleanup(func() { _ = a.telemetry.close(context.Background()) })
	pid := createReadTestProject(t, a, "impl")
	changeID := createReadTestChange(t, a, pid, "build from specification")
	installFakeGit(t, a, nil)
	// The declaration promises a prerequisite from an earlier stage. The
	// external producer finishes without emitting it, so execution must recheck.
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners["script"] = func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}
	})
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "build", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "prepare", Steps: []schema.Step{{Name: "spec", Uses: "script", With: map[string]any{"output": map[string]any{"kind": "spec-final"}}}}},
		{Name: "implement", Steps: []schema.Step{{Name: "build-v1", Uses: "agent/pi", With: map[string]any{"prompt": "build ${{latest.spec-final}}", "requires": []string{"spec-final"}}}}},
	}}})
	run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: changeID, Workflow: "build"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var agentStep *workflow.Step
	for _, step := range snapshot.Steps {
		if step.Name == "build-v1" {
			agentStep = step
		}
	}
	if agentStep == nil {
		t.Fatal("agent step was not admitted")
	}
	runID := run.ID
	if agentStep.Status != workflow.StatusFail {
		t.Fatalf("missing spec-final must fail the step, got %q", agentStep.Status)
	}
	if !strings.Contains(string(agentStep.Evidence), "spec-final") {
		t.Fatalf("the reason must name the missing artifact: %s", agentStep.Evidence)
	}
	// No agent session was ever created (the workspace has no .apptown/sessions).
	if started := workEventCount(t, a, func(e workflow.Event) bool {
		return e.RunID == runID && e.Kind == "step.started" && payloadField(e, "step") == "build-v1"
	}); started != 1 {
		t.Fatalf("exactly one started event (the fast-fail), got %d", started)
	}

	// The actual prerequisite reader now accepts evidence recorded through Work.
	if _, err := a.Work.AttachArtifact(asPerson("bls"), runID, workflow.ArtifactDraft{Kind: "spec-final", Name: "spec", Body: "the spec"}); err != nil {
		t.Fatal(err)
	}
	if missing, err := workflow.MissingArtifactKindsIn(context.Background(), a.DB, pid, []string{"spec-final"}); err != nil || len(missing) != 0 {
		t.Fatalf("present prerequisite: %v %v", missing, err)
	}
	// The attempt never ran a harness, and its span still settles with the
	// step's own outcome rather than vanishing from the trace.
	if err := a.telemetry.providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var settled bool
	for _, sp := range receiver.Spans() {
		if sp.Name == "workflow.step" && sp.Attributes["apptown.step.type"] == "agent/pi" && sp.Attributes["apptown.step.outcome"] == agentStep.Status.String() && agentStep.Status.String() == "fail" && sp.Attributes["apptown.harness.outcome"] == "" {
			settled = true
		}
	}
	if !settled {
		t.Fatalf("no settled span for the failed attempt: %+v", receiver.Spans())
	}
}

// When the step's own settlement fails to persist, the span says so rather
// than reporting the status that was never written. The fault lands on the
// agent's preflight settlement: the script step before it has already
// finished by the time the fault is armed.
func TestAgentPreflightSettlementFailureIsReportedAsSuch(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	a := testApp(t, Config{OTLPEndpoint: receiver.Endpoint()})
	t.Cleanup(func() { _ = a.telemetry.close(context.Background()) })
	pid := createReadTestProject(t, a, "impl")
	changeID := createReadTestChange(t, a, pid, "build from specification")
	installFakeGit(t, a, nil)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners["script"] = func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
			// Armed after this step's own settlement: the next attempt to
			// finish is the agent step's preflight failure.
			dbtest.FailNextStatement(t, a.DB, "-- name: FinishAttempt")
		}
	})
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "build", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "prepare", Steps: []schema.Step{{Name: "spec", Uses: "script", With: map[string]any{"output": map[string]any{"kind": "spec-final"}}}}},
		{Name: "implement", Steps: []schema.Step{{Name: "build-v1", Uses: "agent/pi", With: map[string]any{"prompt": "build ${{latest.spec-final}}", "requires": []string{"spec-final"}}}}},
	}}})
	if _, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: changeID, Workflow: "build"}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	if err := a.telemetry.providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, sp := range receiver.Spans() {
		if sp.Name == "workflow.step" && sp.Attributes["apptown.step.type"] == "agent/pi" {
			if sp.Attributes["apptown.step.outcome"] != "settlement_error" {
				t.Fatalf("a failed settlement was reported as %q", sp.Attributes["apptown.step.outcome"])
			}
			return
		}
	}
	t.Fatalf("no agent step span: %+v", receiver.Spans())
}

// nextFixtureProject is the id the next fixture project takes: projects are
// numbered in the order the fixture makes them, as these tests name them, so
// the first is idstest.N(1).
func nextFixtureProject(t *testing.T, db *database.DB) ids.ID {
	t.Helper()
	var made int64
	// sql-guarantee: a count of every project, deleted ones included, to number the next; no owner read counts them all.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM projects`).Scan(&made); err != nil {
		t.Fatal(err)
	}
	return idstest.N(uint64(made + 1))
}
