package workflowexec

import (
	"context"
	"encoding/json"
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
func TestWorkerAgentRequiresReadableModelAnswer(t *testing.T) {
	for _, row := range []struct {
		name, unread, reason string
		events               []byte
		status               workflow.StepState
	}{
		{name: "unread", unread: "the invocation's events are over the 62 MiB read", reason: "the agent's output could not be read: the invocation's events are over the 62 MiB read", status: workflow.StatusError},
		{name: "refused", events: piRefuses("openai refused the call (HTTP 429)"), reason: "its model never answered: openai refused the call (HTTP 429)", status: workflow.StatusError},
		{name: "zero output", reason: "its model never answered", status: workflow.StatusError},
		{name: "answered", events: piSays("done"), status: workflow.StatusPass},
	} {
		t.Run(row.name, func(t *testing.T) {
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
						task: task, cfg: &agentConfig{needsStarted: []needStarted{{Kind: "mariadb", Env: []string{"DATABASE_URL"}}}, needsRefused: "s3 unavailable"},
						pi:     piResult{Outcome: execution.Outcome{Termination: execution.Exited, ExitCode: 0}, Events: row.events, OutputTokens: sumPiUsage(row.events).AssistantOutput},
						files:  workspaceHarvest{Output: "# completed report"},
						worker: &workerRecord{outputUnread: row.unread},
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
			run, err := work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "write"})
			if err != nil {
				t.Fatal(err)
			}
			work.Wait()
			steps, err := workflow.ReadScopedStepsIn(ctx, f.exec.db, workflow.RunRef{ID: run.ID, ProjectID: f.call.run.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			var evidence struct {
				Needs  needsEvidence `json:"needs"`
				Reason string        `json:"reason"`
			}
			if len(steps) != 1 {
				t.Fatalf("steps: %v", steps)
			}
			if err := json.Unmarshal(steps[0].Evidence, &evidence); err != nil {
				t.Fatal(err)
			}
			if len(evidence.Needs.Started) != 1 || evidence.Needs.Started[0].Kind != "mariadb" || evidence.Needs.Refused != "s3 unavailable" {
				t.Fatalf("recorded needs: %+v", evidence.Needs)
			}
			if got != row.status || evidence.Reason != row.reason {
				t.Fatalf("got %s %q; want %s %q", got, evidence.Reason, row.status, row.reason)
			}
		})
	}
}
