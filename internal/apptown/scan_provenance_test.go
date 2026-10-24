package apptown

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The provenance base is the branch's fork point from main — not the tip the
// landing run started at (which is the change's own head).
func TestProvenanceBaseIsTheForkPoint(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid, fork, git := fleetRepo(t, a, "fp", map[string]string{"a.txt": "one"})
	p := a.projectByID(context.Background(), pid)
	if p == nil {
		t.Fatal("project missing")
	}
	if _, err := a.recordAdoption(p, "edit", "fork-point fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	ch := engineLandingChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	branch := ch.Branch
	def := strings.TrimSpace(git("rev-parse", "--abbrev-ref", "HEAD"))
	git("checkout", "-q", "-b", branch)
	writeRepoFile(t, a, "fp", "b.txt", "two")
	git("add", "-A")
	git("commit", "-q", "-m", "change commit 1")
	tip1 := strings.TrimSpace(git("rev-parse", "HEAD"))
	writeRepoFile(t, a, "fp", "c.txt", "three")
	git("add", "-A")
	git("commit", "-q", "-m", "change commit 2")
	git("checkout", "-q", def)
	git("commit", "-q", "--allow-empty", "-m", "main moved on")

	// Admit the landing run through Work; provenance must still use the branch
	// fork point rather than the run's current branch tip.
	installFakeGit(t, a, func(in workflow.CloneRequest) (workflow.WorkspaceResult, error) {
		return workflow.WorkspaceResult{Path: in.Workspace, Tip: tip1}, nil
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	source := workflowSourceForTest(t, a, run)
	if source.RunBaseSHA != tip1 {
		t.Fatalf("landing run must retain branch tip %s as its run base, got %q", tip1[:8], source.RunBaseSHA)
	}
	if got, err := verification.WorkflowProvenanceBase(t.Context(), a.cfg.dataDir, workflowSourceForTest(t, a, run), "refs/heads/"+branch); err != nil || got != fork {
		t.Fatalf("provenance base must be the fork point %s, not the run's base_sha %s: got %s %v", fork[:8], tip1[:8], got, err)
	}
}
