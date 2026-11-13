package apptown

import (
	"context"
	"database/sql"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
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
	}); started != 4 {
		t.Fatalf("initial failure plus three automatic rounds, all before invoking the agent, got %d", started)
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

// Every public creation path shares this owner command. The replayable wizard
// and the CLI/MCP command both retain the actual creating credential.
func TestNewProjectGetsDefaultFollowsMain(t *testing.T) {
	for _, scenario := range []string{"person", "wizard", "agent token", "no default target", "attached default", "chosen target", "nowhere", "wizard nowhere", "write failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var a *App
			if scenario == "no default target" || scenario == "attached default" {
				a = infrastructureModeApp(t)
			} else {
				a = testApp(t)
			}
			testAdministrator(t, a, "creator")
			pr := access.Principal{ID: testPrincipalOf(t, a.DB, "creator"), Kind: "user", Name: "creator", Via: "token", Purpose: access.TokenCLI, Credential: "token:creator-cli"}
			if scenario == "agent token" {
				pr.Purpose = access.TokenAgent
				pr.Credential = "token:creator-agent"
			}
			targetName := deployment.BuiltInTargetName
			if scenario == "attached default" || scenario == "chosen target" {
				targetName = "apps"
				target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: targetName, Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.Deployment.Targets.Offer(t.Context(), target, "*", deployment.OfferCaps{}, "creator"); err != nil {
					t.Fatal(err)
				}
			}
			in := projectlifecycle.Draft{Slug: "new-app", Name: "New app", Actor: "creator"}
			if scenario == "chosen target" {
				in.Target = targetName
			}
			if scenario == "nowhere" || scenario == "wizard nowhere" {
				in.Target = "none"
			}
			if scenario == "wizard" || scenario == "wizard nowhere" {
				in.RequestID = ids.New(a.cfg.clock.Now())
			}
			if scenario == "write failure" {
				dbtest.FailNextStatement(t, a.DB, "INSERT INTO deployments")
			}
			p, err := a.lifecycle.Create(verifiedContext(t.Context(), pr), in)
			if scenario == "write failure" {
				if err == nil {
					t.Fatalf("creation did not report deployment write failure: %v", err)
				}
				if _, err := a.Projects.BySlug(t.Context(), in.Slug); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("project survived failed creation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			dep, err := a.Deployment.Deployments.ByName(t.Context(), p.ID, "main")
			if scenario == "no default target" || scenario == "nowhere" || scenario == "wizard nowhere" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("deployment without a default target: %+v %v", dep, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if dep.Feed != "main" || dep.FeedConfirmed != (scenario != "agent token") {
				t.Fatalf("default feed: %+v", dep)
			}
			target, err := a.Deployment.Targets.ByID(t.Context(), dep.TargetID)
			if err != nil || target.Name != targetName {
				t.Fatalf("default target: %+v %v", target, err)
			}
			if scenario == "agent token" {
				change, err := a.Work.Create(verifiedContext(t.Context(), pr), workflow.ChangeDraft{ProjectID: p.ID, Title: "First landing"})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
					t.Fatal(err)
				}
				recordFixtureAdoptions(t, a)
				defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "first-build", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "build", Uses: "build"}}}}}})
				a.Work.Stop()
				run, err := a.Work.StartRun(asPerson("creator"), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "first-build"})
				if !errors.Is(err, workflow.ErrDispatchStopped) {
					t.Fatal(err)
				}
				if _, err := a.Deployment.Bundles.Cut(t.Context(), p.ID, "first-tree", map[string]string{"web": "test-image"}, nil, []byte("contract"), deployment.BundleOrigin{ChangeID: change.ID, RunID: run.ID}); err != nil {
					t.Fatal(err)
				}
				if err := a.Deployment.RequestLandingReleases(t.Context(), p.ID, run.ID, change.ID, "", false); err != nil {
					t.Fatal(err)
				}
				requests, err := a.Deployment.Releases.LandingRequests(t.Context(), p.ID, change.ID)
				if err != nil || len(requests) != 1 {
					t.Fatalf("agent landing: %+v %v", requests, err)
				}
				if err := a.Deployment.ExecuteRelease(a.asEngine(t.Context()), requests[0].ID); err == nil || !strings.Contains(err.Error(), "Not yet set to follow main") {
					t.Fatalf("agent landing was not refused as unconfirmed: %v", err)
				}

			}
			if scenario == "wizard" {
				if _, err := a.lifecycle.Create(verifiedContext(context.Background(), pr), in); err != nil {
					t.Fatal(err)
				}
				after, err := a.Deployment.Deployments.ByID(t.Context(), dep.ID)
				if err != nil || after.FeedRevision != dep.FeedRevision {
					t.Fatalf("replay reconfirmed feed: %+v %v", after, err)
				}
			}
		})
	}
}
