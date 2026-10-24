package workflowexec

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
)

// A worker that cannot return the events still returns other result files.
// Written output must not turn that unknown invocation into a passing step:
// its completion, a provider's refusal and its usage are unknown.
//
// Kills: unread events judged only when the agent wrote nothing.
func TestUnreadWorkerEventsCannotPassWithWrittenOutput(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	directory := identity.New(f.exec.db, identity.Config{Projects: project.NewCatalogue(f.exec.db)})
	if err := directory.SeedAdministrator(t.Context(), "reviewer"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reviewer", Via: "session"}, Verified: true})
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "write", Uses: AgentKind}}}}}
	var got workflow.StepState
	work := workflow.New(context.Background(), f.exec.db, workflow.Dependencies{
		Authority: access.Checked{Authority: directory, Projects: project.New(f.exec.db, directory)},
		DataDir:   f.dir, Repositories: &scm.RepositoryLocks{},
		Resolve: func(_ context.Context, _ workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "write", Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
		Automatic: []string{AgentKind},
		Runners: map[string]workflow.Runner{AgentKind: func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			var err error
			got, err = f.exec.recordAgentOutcome(ctx, agentRecord{
				task: task, cfg: &agentConfig{},
				pi:     piResult{Outcome: execution.Outcome{Termination: execution.Exited, ExitCode: 0}},
				files:  workspaceHarvest{Output: "# completed report"},
				worker: &workerRecord{outputUnread: "the invocation's events are over the 62 MiB read"},
			})
			if err != nil {
				t.Error(err)
			}
		}},
	})
	t.Cleanup(work.Stop)
	change, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: f.call.run.ProjectID, Title: "Write a report"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "write"}); err != nil {
		t.Fatal(err)
	}
	work.Wait()
	if got != workflow.StatusError {
		t.Fatalf("unread worker events with written output settled as %s; want error", got)
	}
}
