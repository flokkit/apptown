package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// An agent step of a change on AppTown's own machine is caught up with
// main as on a worker: AppTown merges main into the change's branch before
// the step, the agent works on the merge in the run's folder and is told
// it need not catch up, and the run records the merge.
//
// A folder that cannot be brought to the merge (a file the agent left
// uncommitted is in the way) ends the step before the agent starts; a
// retry while it is still in the way ends the same way; once it is gone, a
// retry starts on the merge already published, with no second merge.
//
// Kills: no merge before a local step (the agent never sees main); a step
// started on a folder that does not hold the merge.
func TestALocalAgentStepStartsOnMainMergedIn(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"work"}`)
	snapshot, err := a.work.Snapshot(a.ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.db, workflow.RunRef{ID: runID, ProjectID: snapshot.Run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	hub, folder := requireLocation(t)(source.Repository(a.dir)), requireLocation(t)(source.Workspace(a.dir))
	// Main moves after the change's branch was made from it.
	if err := os.WriteFile(filepath.Join(hub, "later.txt"), []byte("main moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, hub, "add", "later.txt")
	gittest.RunAsTester(t, hub, "commit", "-qm", "main moves")
	main := gittest.Run(t, hub, "rev-parse", "refs/heads/main")
	// The run's folder has a file of the same name, uncommitted.
	if err := os.WriteFile(filepath.Join(folder, "later.txt"), []byte("left behind\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var steering []string
	pi := installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		body, err := os.ReadFile(filepath.Join(req.Workspace, "later.txt"))
		if err != nil || string(body) != "main moved\n" {
			t.Errorf("the agent's folder does not hold main: %q %v", body, err)
		}
		brief, _ := os.ReadFile(filepath.Join(req.Workspace, ".apptown", "steering.md"))
		steering = append(steering, string(brief))
		if err := os.WriteFile(filepath.Join(req.Workspace, "work.txt"), []byte("done\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return execution.Outcome{Output: piSays("done")}
	})
	merged := func() int {
		return len(runIDEvents(t, a, runID, repositoryedit.MainMergedNotice))
	}
	attempt := func(want workflow.StepState) map[string]any {
		t.Helper()
		if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
			t.Fatal(err)
		}
		status, evidence := stepStatus(t, a, stepID)
		if status != want {
			t.Fatalf("the step is %s, want %s: %v", status, want, evidence)
		}
		return evidence
	}
	retry := func() {
		t.Helper()
		if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		evidence := attempt(workflow.StatusError)
		if reason, _ := evidence["reason"].(string); !strings.Contains(reason, "the run's folder could not be brought to the change's branch") {
			t.Fatalf("attempt %d: %v", i+1, evidence)
		}
		if n := len(pi.requests()); n != 0 {
			t.Fatalf("attempt %d: the agent was started %d times on a folder without the merge", i+1, n)
		}
		if n := merged(); n != 1 {
			t.Fatalf("attempt %d: %d merges recorded, want 1", i+1, n)
		}
		retry()
	}
	merge := gittest.Run(t, hub, "rev-parse", "refs/heads/"+source.Branch)
	if parents := strings.Fields(gittest.Run(t, hub, "rev-list", "--parents", "-n", "1", merge)); len(parents) != 3 || parents[2] != main {
		t.Fatalf("the branch is not a merge of main: %v", parents)
	}
	if err := os.Remove(filepath.Join(folder, "later.txt")); err != nil {
		t.Fatal(err)
	}
	attempt(workflow.StatusPass)
	if n := len(pi.requests()); n != 1 {
		t.Fatalf("the agent ran %d times", n)
	}
	if n := merged(); n != 1 {
		t.Fatalf("%d merges recorded, want the one", n)
	}
	tip := gittest.Run(t, hub, "rev-parse", "refs/heads/"+source.Branch)
	if !gitexec.HubAncestor(t.Context(), hub, merge, tip) {
		t.Fatal("the step's work does not build on the merge")
	}
	if len(steering) != 1 || !strings.Contains(steering[0], mainMergeBrief) {
		t.Fatalf("the agent is not told AppTown merges main: %q", steering)
	}
}
