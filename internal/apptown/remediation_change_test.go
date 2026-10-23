package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestRemediationTitleCannotChangeAcceptance(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "test")
	pid, _, _ := fleetRepo(t, a, "readable", map[string]string{"README.md": "example"})
	sc := conformance.DeclaredCheck{Block: "build/container-image", Version: 3, Check: conformance.CheckDeclaration{
		Name: "runs-as-non-root", Title: "Run the application without root privileges", Expects: "The final Dockerfile stage declares an unprivileged user."}}
	ch, err := a.Work.Create(a.asAppTown(context.Background(), pid, "test"), workflow.ChangeDraft{ProjectID: pid, Type: "remediation", Title: remediation.ChangeTitle(sc), Body: "A remediation brief", Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{remediation.AcceptanceCheck(sc)}}})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Title != sc.Check.Title {
		t.Fatalf("title: %q", ch.Title)
	}
	// Make an ordinary title edit, including a string that used to encode authority.
	title := "Conform to other/block@1 · unrelated"
	rev := ch.EditRevision
	if _, err := a.Work.Edit(asPerson("test"), workflow.ChangeEdit{ProjectID: pid, ID: ch.ID, Title: &title, ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	ch, err = a.changeByID(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := workflow.AcceptanceScopeOf(ch)
	if !target.Covers("guidance|build/container-image@v3|runs-as-non-root|Dockerfile") || target.Covers("guidance|other/block@v1|unrelated|x") {
		t.Fatalf("title changed acceptance: %+v", target)
	}
	proof, err := changeEvidenceForTest(t, a, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Merged || len(proof.Releases) != 0 || len(proof.Targets) != 1 || proof.Targets[0].Version != 3 {
		t.Fatalf("recorded evidence: %+v", proof)
	}
	legacy := &workflow.Change{Type: "remediation", Title: "Conform to old/block@2 · old-check"}
	if workflow.AcceptanceScopeOf(legacy).Covers("guidance|old/block@v2|old-check|x") {
		t.Fatal("a title must not supply acceptance authority")
	}
}

func completeEvidenceRelease(t *testing.T, a *App, ctx context.Context, changeID ids.ID) *workflow.Run {
	t.Helper()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: changeID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var deployStep, approvalStep ids.ID
	for _, step := range snapshot.Steps {
		switch step.Name {
		case "deploy":
			deployStep = step.ID
		case "accept":
			approvalStep = step.ID
		}
	}
	if deployStep.IsZero() || approvalStep.IsZero() {
		t.Fatalf("release steps not materialized: %+v", snapshot.Steps)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), deployStep); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Bundles.Cut(ctx, run.ProjectID, "tree", map[string]string{"web": "image"}, nil, []byte("contract"), deployment.BundleOrigin{RunID: run.ID, ChangeID: run.ChangeID}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Approve(ctx, workflow.Approval{StepID: approvalStep, Decision: workflow.VoteApprove}); err != nil {
		t.Fatal(err)
	}
	if out, err := a.Work.AdvanceFrom(ctx, run); err != nil || !out.Advanced {
		t.Fatalf("complete landing: %+v %v", out, err)
	}
	return run
}

func TestChangeEvidenceRequiresRecordedMergeAndReleaseMembership(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	project, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "receipts", Name: "Receipts", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pid := project.ID
	if _, err := a.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "test", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: pid.String(), By: "test", Origin: "fixture"}, ""); err != nil {
		t.Fatal(err)
	}
	sc := conformance.DeclaredCheck{Block: "example", Version: 1, Check: conformance.CheckDeclaration{Name: "check"}}
	ch, err := a.Work.Create(a.asAppTown(context.Background(), pid, "test"), workflow.ChangeDraft{ProjectID: pid, Type: "remediation", Title: remediation.ChangeTitle(sc), Body: "brief", Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{remediation.AcceptanceCheck(sc)}}})
	if err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "checks", Steps: []schema.Step{
			{Name: "deploy", Uses: "deploy", With: map[string]any{"deployment": "main"}},
			{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"test"}, "means": "the release evidence is ready"}},
		}}},
	}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Git = fakeGit{}
		deps.Runners = map[string]workflow.Runner{"deploy": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	a.Work.Stop()
	ctx := asPerson("test")
	run := completeEvidenceRelease(t, a, ctx, ch.ID)
	other, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Type: "feature", Title: "other released work"})
	if err != nil {
		t.Fatal(err)
	}
	completeEvidenceRelease(t, a, ctx, other.ID)
	proof, err := changeEvidenceForTest(t, a, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Merged || len(proof.Releases) != 1 || proof.Releases[0].Number != 1 || proof.Releases[0].Serving {
		t.Fatalf("completion/release history overstated evidence: %+v", proof)
	}
	if err := workflow.RecordNotice(ctx, a.DB, workflow.Notice{RunID: run.ID, Kind: "merge.completed", Payload: json.RawMessage(`{"sha":"abc123"}`), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	proof, err = changeEvidenceForTest(t, a, ch.ID)
	if err != nil || !proof.Merged {
		t.Fatalf("merge receipt missing: %+v, %v", proof, err)
	}
}

type evidenceWire struct {
	Targets  []workflow.AcceptanceCheck
	Merged   bool
	Releases []struct {
		Number  int64
		Serving bool
	}
}

func changeEvidenceForTest(t *testing.T, a *App, id ids.ID) (evidenceWire, error) {
	t.Helper()
	response := authzCall(a.Handler(), "test", "GET", fmt.Sprintf("/api/v1/changes/%d", id), "")
	var out struct{ Evidence evidenceWire }
	if response.Code != 200 {
		return out.Evidence, fmt.Errorf("change HTTP %d: %s", response.Code, response.Body.String())
	}
	err := json.Unmarshal(response.Body.Bytes(), &out)
	return out.Evidence, err
}
