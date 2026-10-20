package forge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// A required check in an earlier stage must run again, or hold the merge,
// when catch-up in a later stage selects a new candidate.
func TestAnEarlierCheckInvalidatedByANewCandidateHoldsTheMerge(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	run, source := reviewLanding(t, a, nil, map[string]string{"value.txt": "good\n"}, func(ids.ID) {
		defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
			{Name: "checks", Steps: []schema.Step{{Name: "source-check", Uses: "script", With: map[string]any{"run": `test "$(cat value.txt)" = good`}}}},
			{Name: "catchup", Steps: []schema.Step{{Name: "catch-up", Uses: "reconcile"}}},
			{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}},
		}}})
	})
	checked := reviewExecute(t, a, run, "source-check")
	if checked.Status != workflow.StatusPass {
		t.Fatalf("check: %s %s", checked.Status, checked.Evidence)
	}
	if _, err := advanceAs(a, run.ID, "system"); err != nil {
		t.Fatal(err)
	}
	// The change's worker publishes more work while the landing runs: the
	// catch-up pins it as a new candidate.
	publishChangeWorkForTest(t, a, source, "value.txt", "bad\n", "new candidate would fail the earlier check")
	caught := reviewExecute(t, a, run, "catch-up")
	if caught.Status != workflow.StatusPass {
		t.Fatalf("catch-up: %s %s", caught.Status, caught.Evidence)
	}
	saved, err := workflow.ReadStepIn(context.Background(), a.DB, checked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != workflow.StatusPending {
		t.Fatalf("expected candidate invalidation, got %s", saved.Status)
	}
	source = mergeSourceForTest(t, a, run)
	result, err := mergeEffectForTest(t, a, source)
	if err == nil && result.Merged {
		t.Fatalf("merged %q with earlier required source-check still %s", gittest.Run(t, source.Repository(a.cfg.dataDir), "show", "HEAD:value.txt"), saved.Status)
	}
}

// Calling a pipeline must not escape the parent's checked source boundary.
func TestACheckedLandingDoesNotMergeOnACalledWorkflowsChecks(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	run, source := reviewLanding(t, a, nil, map[string]string{"value.txt": "bad\n"}, func(ids.ID) {
		defineWorkflows(t, a,
			workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
				{Name: "checks", Steps: []schema.Step{{Name: "call", Uses: "workflow/call", With: map[string]any{"workflow": "source-check"}}}},
				{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}},
			}}},
			workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "source-check", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "verify", Uses: "script", With: map[string]any{"run": `test "$(cat value.txt)" = good; result=$?; git checkout -- value.txt; exit "$result"`}}}}}}})
	})
	if err := os.WriteFile(filepath.Join(source.Workspace(a.cfg.dataDir), "value.txt"), []byte("good\n"), 0644); err != nil {
		t.Fatal(err)
	}
	call := reviewExecute(t, a, run, "call")
	var ev struct {
		ChildRunID ids.ID `json:"childRunId"`
	}
	if err := json.Unmarshal(call.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ChildRunID.IsZero() {
		t.Fatalf("call: %s %s", call.Status, call.Evidence)
	}
	child, err := workflow.ReadRunIn(context.Background(), a.DB, ev.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	check := reviewExecute(t, a, child, "verify")
	if check.Status != workflow.StatusPass {
		return
	} // Correctly refused or checked the candidate.
	if _, err := advanceAs(a, child.ID, "system"); err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(context.Background(), a.DB, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != workflow.StatusPass {
		t.Fatalf("child did not complete call: %s %s", saved.Status, saved.Evidence)
	}
	result, err := mergeEffectForTest(t, a, mergeSourceForTest(t, a, run))
	if err == nil && result.Merged {
		t.Fatalf("landed %q after child checked live good bytes with evidence %s", gittest.Run(t, source.Repository(a.cfg.dataDir), "show", "HEAD:value.txt"), check.Evidence)
	}
}

// Settings changed on GitLab between AppTown reading them and the merge
// can land a tree nobody checked; AppTown cannot prevent that race on the
// provider. It detects it: the landing is not recorded as merged and the
// forge's main does not follow.
func TestAProviderSettingChangeCannotLandAnUncheckedTree(t *testing.T) {
	t.Parallel()
	a, source, provider, clone := mergeRequestWorld(t)
	remote := gittest.Run(t, clone, "remote", "get-url", "origin")
	other := filepath.Join(t.TempDir(), "other")
	gittest.Run(t, t.TempDir(), "clone", "-q", remote, other)
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("not in candidate"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, other, "add", "other.txt")
	gittest.Run(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "target moves")
	seam := mergesFor(a)
	seam.driver = func() scm.Driver {
		return afterMergePreparation{Driver: mergeEffectsFor(a).Driver(), after: func() {
			mergeMethod(provider, "merge")
			gittest.Run(t, other, "push", "-q", "origin", source.DefaultBranch)
		}}
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	repo := source.Repository(a.cfg.dataDir)
	before := gittest.Run(t, repo, "rev-parse", "refs/heads/"+source.DefaultBranch)
	result, err := seam.execute(context.Background(), advanceWorkflowMerge(run, source))
	if err == nil || result.Merged {
		t.Fatalf("an unchecked provider merge was reported as landed: %+v %v", result, err)
	}
	if after := gittest.Run(t, repo, "rev-parse", "refs/heads/"+source.DefaultBranch); after != before {
		t.Fatalf("AppTown's main followed an unchecked provider merge: %s -> %s", before, after)
	}
}

type acceptedLandingMeans struct{}

func (acceptedLandingMeans) LandingMeans(context.Context, ids.ID) ([]string, error) {
	return []string{"merge the accepted work"}, nil
}

type alwaysCanLand struct{}

func (alwaysCanLand) CanLand(context.Context, ids.ID) (bool, error) { return true, nil }

// A clean, already-published commit made after acceptance is not a sweep.
// It still needs fresh assent; admission time cannot substitute for acceptance.
func TestAnAcceptanceCarriesOnlyToTheCommitItWasGivenOn(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	pid := plainTestProject(t, a.DB, "carry-source", "Carry source")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a,
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "I accept this work"}}}}}}},
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
	work, err := a.Work.StartRun(a.asForge(context.Background(), pid, "review"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	source := workflowSourceForTest(t, a, work)
	ws := source.Workspace(a.cfg.dataDir)
	writeCommit := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, ws, "add", "work.txt")
		gittest.Run(t, ws, "commit", "-qm", body)
		if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: pid, Repository: source.Repository(a.cfg.dataDir), Branch: source.Branch, From: ws, Grant: scm.Grant{Holder: scm.RunHolder(work.ID)}}); err != nil {
			t.Fatal(err)
		}
	}
	writeCommit("accepted X")
	acceptedSHA := gittest.Run(t, ws, "rev-parse", "HEAD")
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
	if _, err := advanceAs(a, work.ID, "system"); err != nil {
		t.Fatal(err)
	}
	writeCommit("unaccepted Y")
	if _, err := a.Work.AdvanceAcceptedLanding(context.Background(), work.ID, alwaysCanLand{}); !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("creating landing: %v", err)
	}
	runs, err := a.Work.ChangeRuns(asPerson("bls"), ch.ID, workflow.ChangeRuns{RootsOnly: true, Limit: 1})
	if err != nil || len(runs) != 1 || runs[0].ID == work.ID {
		t.Fatalf("the change's newest run is not the landing: %v %v", runs, err)
	}
	landingID := runs[0].ID
	landing, err := workflow.ReadRunIn(context.Background(), a.DB, landingID)
	if err != nil {
		t.Fatal(err)
	}
	pin := mergeSourceForTest(t, a, landing).Landing
	if pin.Candidate == acceptedSHA || pin.Swept {
		t.Fatalf("bad fixture: %+v", pin)
	}
	check := reviewExecute(t, a, landing, "tests")
	if check.Status != workflow.StatusPass {
		t.Fatalf("tests: %s %s", check.Status, check.Evidence)
	}
	// Apply the handoff and advance through the clean gates to carried approval.
	if _, err := a.Work.AdvanceAcceptedLanding(context.Background(), work.ID, alwaysCanLand{}); err != nil {
		t.Fatal(err)
	}
	if _, err := advanceAs(a, landingID, "system"); err != nil {
		t.Fatal(err)
	}
	snap, err = a.Work.Snapshot(asPerson("bls"), landingID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == "approve" && s.Status == workflow.StatusPass {
			t.Fatalf("accepted %s, but carried that approval to %s: %s", acceptedSHA, pin.Candidate, s.Evidence)
		}
	}
}

// Committed work whose publication failed when a landing started is still
// published by the retried landing: it does not silently drop out.
func TestARetriedLandingIncludesWorkWhosePublicationFailed(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := plainTestProject(t, a.DB, "sweep-retry", "Sweep retry")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "checks", Steps: []schema.Step{{Name: "tests", Uses: "tests"}}},
		{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}},
	}}})
	if err := a.lifecycle.SelectLane(t.Context(), pid, "land", true, "review"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
	repo := a.projectRepoPath(p.Slug)
	if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: pid, Repository: repo, Branch: ch.Branch, From: ws, Grant: scm.Grant{Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "swept.txt"), []byte("the work requested for landing"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "swept.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the work requested for landing")
	swept := gittest.Run(t, ws, "rev-parse", "HEAD")
	a.Work.Stop()
	dbtest.FailNextStatement(t, a.DB, "-- name: BeginPublication")
	ctx := a.asForge(context.Background(), pid, "review")
	if run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"}); err == nil || run != nil {
		t.Fatalf("first start did not fail: %+v %v", run, err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		return
	} // Holding admission is safe.
	if run == nil {
		t.Fatal("retry returned no run")
	}
	source := mergeSourceForTest(t, a, run)
	if source.Landing.Candidate != swept {
		t.Fatalf("retry admitted old candidate %s and omitted the committed work %s", source.Landing.Candidate, swept)
	}
}
