package workflowexec

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

// These checks isolate queue accounting from the external update command.
// All state is admitted and read through the workflow owner.
func TestReviewHumanActEndsQueueEpisode(t *testing.T) {
	for _, act := range []string{"skip", "retry", "edit", "skip-stopped"} {
		t.Run(act, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "attempt", Uses: ScriptKind, With: map[string]any{"run": "false"}}}}}}
			f, _ := newTaskAppTown(t, "review-episode", spec)
			f.scriptRunner = func(task *workflow.Execution) {
				if _, err := task.Claim(); err != nil {
					t.Fatal(err)
				}
				if err := task.StartFix(nil); err != nil {
					t.Fatal(err)
				}
				task.Finish(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "update failed"}, nil))
			}
			var last *workflow.Step
			var changeID ids.ID
			for n := 0; n < 3; n++ {
				run := queueRun(t, f, "review-episode", "updates")
				changeID = run.ChangeID
				last = queueSteps(t, f, run)["attempt"]
				queueExecute(t, f, last)
				if act == "skip" && n < 2 {
					if err := f.work.SkipUpdate(f.ctx, f.projectID, "updates", run.ChangeID); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := 1 // Two explicit skips ended the earlier episodes.
			if act == "skip-stopped" {
				before, err := f.work.QueueState(f.ctx, f.projectID, "updates")
				if err != nil || !before.Stopped() {
					t.Fatalf("before skip: %+v %v", before, err)
				}
				if err := f.work.SkipUpdate(f.ctx, f.projectID, "updates", changeID); err != nil {
					t.Fatal(err)
				}
				want = 0
			}
			if act == "retry" {
				if _, err := f.work.RetryStep(f.ctx, workflow.RetryCommand{StepID: last.ID}); err != nil {
					t.Fatal(err)
				}
				want = 0 // A person has taken over the held work.
			}
			if act == "edit" {
				ch, err := f.work.Change(f.ctx, changeID)
				if err != nil {
					t.Fatal(err)
				}
				title := "A person is fixing this"
				if _, err := f.work.Edit(f.ctx, workflow.ChangeEdit{ID: changeID, Title: &title, ExpectedRevision: &ch.EditRevision}); err != nil {
					t.Fatal(err)
				}
				want = 0
			}
			state, err := f.work.QueueState(f.ctx, f.projectID, "updates")
			if err != nil {
				t.Fatal(err)
			}
			if state.Failed != want || state.Stopped() {
				t.Fatalf("after %s: failed=%d stopped=%v state=%s; want failed=%d and not stopped", act, state.Failed, state.Stopped(), state.State, want)
			}
		})
	}
}
