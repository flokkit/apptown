package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/dbschema"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/progress"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) {
	code := databaseSuite.Run(m)
	gittest.RemoveTemplates()
	os.Exit(code)
}

type reviewProcess func(execution.Request) execution.Outcome

func (p reviewProcess) Run(_ context.Context, _ string, req execution.Request) execution.Outcome {
	return p(req)
}

type noReviewCLI struct{}

func (noReviewCLI) Path() (string, error) { return "", errors.New("no CLI in runner fixture") }

// testExecutor is an Executor over db whose agents are run by process on the
// local backend, with the usage ledger it records into.
func testExecutor(db *database.DB, dir string, process Process) *Executor {
	tokens := &identity.RuntimeTokens{}
	if err := tokens.Open(filepath.Join(dir, identity.RuntimeTokenKeyFile)); err != nil {
		panic(err)
	}
	return New(db, Dependencies{
		Models:   model.NewSelection(model.New(db), "fake/model"),
		Capacity: model.NewScheduler(nil, 1),
		Tokens:   tokens,
		Progress: &progress.Feeds{},
		Docker:   &docker.Scope{Client: dockerfake.New()},
		Secrets:  secret.NewService(db, secret.Config{DataDir: dir}),
		Ledger:   usage.New(db),
		CLI:      noReviewCLI{},
		Process:  process,
	}, Config{DataDir: dir, Backend: execution.Local, Image: "apptown-agent"})
}

// Work admits and dispatches real executions into a bounded channel. Once its
// dispatcher is idle, the test invokes the runner with that admitted input.
// This controls boot/review ordering without stopping or rebuilding Work.
// These verdict tests do not need a Git repository.
// Root keeps the tests of repository setup and live CLI/token composition.
func reviewFixture(t *testing.T, boot bool, process reviewProcess) (*workflow.Service, context.Context, map[string]ids.ID, func(string), *usage.Ledger) {
	t.Helper()
	return reviewFixtureWith(t, boot, process, nil)
}

// reviewFixtureWith lets a test replace the executor's private collaborators
// (the scope reader) before any step runs.
func reviewFixtureWith(t *testing.T, boot bool, process reviewProcess, adjust func(*Executor)) (*workflow.Service, context.Context, map[string]ids.ID, func(string), *usage.Ledger) {
	t.Helper()
	steps := []schema.Step{{Name: "review", Uses: ReviewKind}}
	if boot {
		steps = append([]schema.Step{{Name: "boot", Uses: "preview"}}, steps...)
	}
	return reviewFixtureSteps(t, steps, process, adjust)
}

// reviewFixtureSteps admits a run of one stage of steps (review and preview
// steps dispatched to the test, approvals left to a person).
func reviewFixtureSteps(t *testing.T, steps []schema.Step, process reviewProcess, adjust func(*Executor)) (*workflow.Service, context.Context, map[string]ids.ID, func(string), *usage.Ledger) {
	t.Helper()
	return reviewFixtureFor(t, project.Ordinary, steps, process, adjust)
}

// reviewFixtureFor is reviewFixtureSteps in a project made with purpose.
func reviewFixtureFor(t *testing.T, purpose project.Purpose, steps []schema.Step, process reviewProcess, adjust func(*Executor)) (*workflow.Service, context.Context, map[string]ids.ID, func(string), *usage.Ledger) {
	t.Helper()
	boot := slices.ContainsFunc(steps, func(s schema.Step) bool { return s.Uses == "preview" })
	db := databaseSuite.New(t)
	dir := t.TempDir()
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "reviewer"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reviewer", Via: "session"}, Verified: true})
	projectID, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "review", Name: "Review", Purpose: purpose})
		if err != nil {
			return ids.Nil, err
		}
		if err := adoption.InitializeIn(ctx, tx, id, 0, "", ""); err != nil {
			return ids.Nil, err
		}
		return id, adoption.RecordIn(ctx, tx, id, 1, adoption.AdoptionCreate, "review fixture", adoption.EmptyReceipt(), "reviewer", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := testExecutor(db, dir, process)
	if adjust != nil {
		adjust(executor)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: steps}}}
	// Only review and preview steps are dispatched; an approval waits for a
	// person.
	dispatched := 0
	for _, s := range steps {
		if s.Uses == ReviewKind || s.Uses == "preview" {
			dispatched++
		}
	}
	ready := make(chan *workflow.Execution, dispatched)
	capture := func(task *workflow.Execution) {
		select {
		case ready <- task:
		default:
			t.Error("dispatched more executions than declared steps")
		}
	}
	runners := map[string]workflow.Runner{ReviewKind: executor.Review, "preview": func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"app did not come up in a cold container"}`)}, nil); err != nil {
			t.Error(err)
		}
	}}
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority: access.Checked{Authority: directory, Projects: project.New(db, directory)},
		DataDir:   dir, Repositories: &scm.RepositoryLocks{},
		Resolve: func(_ context.Context, _ workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "review", Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
		Automatic: []string{ReviewKind, "preview"},
		Runners: map[string]workflow.Runner{
			ReviewKind: capture,
			"preview":  capture,
		},
	})
	t.Cleanup(work.Stop)
	change, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Title: "Review the change"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "review"})
	if err != nil {
		t.Fatal(err)
	}
	work.Wait()
	snapshot, err := work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	stepIDs := map[string]ids.ID{}
	for _, step := range snapshot.Steps {
		stepIDs[step.Name] = step.ID
	}
	if stepIDs["review"].IsZero() || boot && stepIDs["boot"].IsZero() {
		t.Fatalf("missing admitted steps: %+v", snapshot.Steps)
	}
	if len(ready) != dispatched {
		t.Fatalf("dispatched %d steps, want %d", len(ready), dispatched)
	}
	pending := map[string]*workflow.Execution{}
	for range dispatched {
		task := <-ready
		pending[task.Step().Name] = task
	}
	return work, ctx, stepIDs, func(name string) {
		task := pending[name]
		if task == nil {
			t.Fatalf("no admitted execution for %q", name)
		}
		runners[task.Step().Type](task)
	}, executor.ledger
}

func reviewFile(t *testing.T, req execution.Request, verdict string) execution.Outcome {
	t.Helper()
	if err := os.WriteFile(filepath.Join(req.Workspace, ".apptown", "review.json"), []byte(verdict), 0600); err != nil {
		t.Fatal(err)
	}
	return execution.Outcome{Output: []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}` + "\n")}
}
