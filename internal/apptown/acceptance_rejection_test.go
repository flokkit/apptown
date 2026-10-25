package apptown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// Binding an approving acceptance to source must not prevent another eligible
// person rejecting the still-pending quorum. A rejection supplies no assent.
func TestAnEligiblePersonCanRejectAPartlyGivenAcceptance(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	testAdministrator(t, a, "reviewer")
	pid := plainTestProject(t, a.DB, "carry-source", "Carry source")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a,
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "I accept this work", "quorum": 2}}}}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
			{Name: "checks", Steps: []schema.Step{{Name: "tests", Uses: "tests"}}},
			{Name: "approve", Steps: []schema.Step{{Name: "approve", Uses: "approval", With: map[string]any{"means": "merge the accepted work"}}}},
			{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}},
		}}})
	if err := a.lifecycle.SelectLane(t.Context(), pid, "land", true, "review"); err != nil {
		t.Fatal(err)
	}
	rebuildWork(t, a, func(d *workflow.Dependencies) { d.LandingMeans = acceptedLandingMeans{} })
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	work, err := a.Work.StartRun(a.asAppTown(context.Background(), pid, "review"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	source := workflowSourceForTest(t, a, work)
	ws := requireLocation(t)(source.Workspace(a.cfg.dataDir))
	writeCommit := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, ws, "add", "work.txt")
		gittest.Run(t, ws, "commit", "-qm", body)
		if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: pid, Repository: requireLocation(t)(source.Repository(a.cfg.dataDir)), Branch: source.Branch, From: ws, Grant: scm.Grant{Holder: scm.RunHolder(work.ID)}}); err != nil {
			t.Fatal(err)
		}
	}
	writeCommit("accepted X")
	snap, err := a.Work.Snapshot(asPerson("bls"), work.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var acceptID ids.ID
	for _, s := range snap.Steps {
		if s.Name == "accept" {
			acceptID = s.ID
		}
	}
	if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: acceptID, Decision: workflow.VoteApprove, Shown: []string{"merge the accepted work"}, Adds: shownAdds(t, a, asPerson("bls"), acceptID)}); err != nil {
		t.Fatal(err)
	}
	step, err := a.Work.Approve(asPerson("reviewer"), workflow.Approval{StepID: acceptID, Decision: workflow.VoteReject, Comment: "This work needs revision"})
	if err != nil {
		t.Fatalf("a second person must be able to reject a pending acceptance after its first source-bound vote: %v", err)
	}
	if step.Status != workflow.StatusFail {
		t.Fatalf("rejection left acceptance %s, want fail", step.Status)
	}
}
