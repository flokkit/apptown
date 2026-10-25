package apptown

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/gitlab/gitlabtest"
	"github.com/flokkit/apptown/internal/ids"
)

// mergeRequestProject is the GitLab project the fixture's merge request is in.
const mergeRequestProject = "71"

// mergeRequestWorld lands a checked landing through a fake GitLab that merges
// by the project's merge method, as GitLab does: "ff" only fast-forwards,
// "rebase_merge" (semi-linear) refuses a branch not up to date with its
// target and then makes a merge commit, and "merge" always makes one. The
// merge request's head follows the remote branch.
func mergeRequestWorld(t *testing.T) (*App, workflow.MergeSource, *gitlabtest.Server, string) {
	t.Helper()
	a, source := mergeSettlementFixture(t)
	testAdministrator(t, a, "bls")
	artifact, err := a.Work.AttachArtifact(asPerson("bls"), source.RunID, workflow.ArtifactDraft{Kind: "mr", Name: "!1", Body: mergeRequestProject})
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source = mergeSourceForTest(t, a, run)
	repo := requireLocation(t)(source.Repository(a.cfg.dataDir))
	remote := filepath.Join(t.TempDir(), "remote.git")
	gittest.Run(t, t.TempDir(), "clone", "--bare", repo, remote)
	gittest.Run(t, repo, "remote", "add", "origin", remote)
	provider := filepath.Join(t.TempDir(), "provider")
	gittest.Run(t, t.TempDir(), "clone", remote, provider)
	gittest.Run(t, provider, "config", "user.name", "Provider")
	gittest.Run(t, provider, "config", "user.email", "provider@example.invalid")
	fake := gitlabtest.New(t)
	fake.Put(mergeRequestProject, gitlab.MergeRequest{IID: 1, State: "opened", SourceBranch: source.Branch, TargetBranch: source.DefaultBranch})
	fake.OnRead = func(_ string, mr *gitlab.MergeRequest) {
		if mr.State == "opened" {
			mr.SHA = gittest.Run(t, remote, "rev-parse", "refs/heads/"+source.Branch)
		}
	}
	fake.OnBranch = func(_, branch string) (string, error) {
		return gitexec.Run(context.Background(), remote, "rev-parse", "refs/heads/"+branch)
	}
	fake.OnMerge = func(m gitlabtest.Merge, mr *gitlab.MergeRequest) error {
		if m.Squash {
			return &gitlabtest.Refusal{Status: http.StatusConflict, Message: "squash"}
		}
		if _, err := gitexec.Run(context.Background(), provider, "fetch", "origin"); err != nil {
			return err
		}
		if _, err := gitexec.Run(context.Background(), provider, "reset", "--hard", "origin/"+source.DefaultBranch); err != nil {
			return err
		}
		merge := []string{"merge", "--no-ff", "origin/" + source.Branch, "-m", m.Message}
		switch m.Settings.MergeMethod {
		case "ff":
			merge = []string{"merge", "--ff-only", "origin/" + source.Branch}
		case "rebase_merge":
			if _, err := gitexec.Run(context.Background(), provider, "merge-base", "--is-ancestor", "origin/"+source.DefaultBranch, "origin/"+source.Branch); err != nil {
				return &gitlabtest.Refusal{Status: http.StatusMethodNotAllowed, Message: "Branch cannot be merged: it must be rebased"}
			}
		}
		if out, err := gitexec.Run(context.Background(), provider, merge...); err != nil {
			return &gitlabtest.Refusal{Status: http.StatusMethodNotAllowed, Message: "Branch cannot be merged: " + out}
		}
		if out, err := gitexec.Run(context.Background(), provider, "push", "origin", source.DefaultBranch); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		result, err := gitexec.Run(context.Background(), provider, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if result != m.SHA {
			mr.MergeCommitSHA = result
		}
		return nil
	}
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("GITLAB_URL="+fake.URL+"\nGITLAB_TOKEN="+gitlabtest.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment := configureRepositoryProviderFixture(t, a, source.ProjectID, fake, remote)
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return scm.RecordMergeRequestIdentityIn(t.Context(), tx, artifact, attachment.Identity)
	}); err != nil {
		t.Fatal(err)
	}
	source = mergeSourceForTest(t, a, run)
	return a, source, fake, provider
}

// mergeMethod sets how the fixture's GitLab project merges.
func mergeMethod(fake *gitlabtest.Server, method string) {
	no := false
	fake.Settings(gitlab.MergeSettings{MergeMethod: method, SquashOption: "default_off", AutomaticRebase: &no})
}

// A merge request lands a checked landing when GitLab guarantees the merge
// is exactly the candidate: fast-forward lands the candidate itself,
// semi-linear a merge commit with the candidate's tree. Either way the
// AppTown's main follows GitLab's.
func TestAMergeRequestLandsExactlyTheCheckedCandidate(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"ff", "rebase_merge"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			a, source, provider, _ := mergeRequestWorld(t)
			mergeMethod(provider, method)
			result, err := mergeEffectForTest(t, a, source)
			if err != nil || !result.Merged {
				t.Fatalf("merge request did not land: %+v %v", result, err)
			}
			repo := requireLocation(t)(source.Repository(a.cfg.dataDir))
			landed := gittest.Run(t, repo, "rev-parse", "refs/heads/"+source.DefaultBranch)
			if method == "ff" && landed != source.Landing.Candidate {
				t.Fatalf("fast-forward landed %s, not the candidate %s", landed, source.Landing.Candidate)
			}
			if got, want := gittest.Run(t, repo, "rev-parse", landed+"^{tree}"), gittest.Run(t, repo, "rev-parse", source.Landing.Candidate+"^{tree}"); got != want {
				t.Fatalf("landed tree %s is not the candidate's %s", got, want)
			}
			wantLandingRecordedMain(t, a, source.ProjectID, repo, source.DefaultBranch, source.RunID)
		})
	}
}

// Main moving on GitLab after the checks is refused before merging, as a
// local landing is when main moves.
func TestAMergeRequestIsRefusedWhenMainMovedOnTheProvider(t *testing.T) {
	t.Parallel()
	a, source, provider, clone := mergeRequestWorld(t)
	if err := os.WriteFile(filepath.Join(clone, "other.txt"), []byte("not checked"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, clone, "add", "other.txt")
	gittest.Run(t, clone, "commit", "-qm", "main moves on GitLab")
	gittest.Run(t, clone, "push", "-q", "origin", source.DefaultBranch)
	result, err := mergeEffectForTest(t, a, source)
	var moved *scm.LandingMoved
	if !errors.As(err, &moved) || moved.What != "main" || result.Merged {
		t.Fatalf("merge request was not refused after main moved: %+v %v", result, err)
	}
	if merges := provider.Merges(); len(merges) != 0 {
		t.Fatalf("the provider was asked to merge %d times", len(merges))
	}
}

// Confirmation must validate the exact revision it moves local main to.
func TestAMergeRequestMovesMainOnlyToTheCheckedLanding(t *testing.T) {
	t.Parallel()
	a, source, provider, clone := mergeRequestWorld(t)
	gittest.Run(t, clone, "merge", "--ff-only", "origin/"+source.Branch)
	candidate := gittest.Run(t, clone, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(clone, "unchecked.txt"), []byte("later work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, clone, "add", "unchecked.txt")
	gittest.Run(t, clone, "commit", "-qm", "later unchecked main")
	gittest.Run(t, clone, "push", "origin", source.DefaultBranch)
	provider.Update(mergeRequestProject, 1, func(mr *gitlab.MergeRequest) {
		mr.State, mr.SHA, mr.MergeCommitSHA = "merged", source.Landing.Candidate, candidate
	})
	result, err := mergeEffectForTest(t, a, source)
	if err != nil {
		return
	}
	repo := requireLocation(t)(source.Repository(a.cfg.dataDir))
	actual := gittest.Run(t, repo, "rev-parse", source.DefaultBranch+"^{tree}")
	checked := gittest.Run(t, repo, "rev-parse", source.Landing.Candidate+"^{tree}")
	if result.Merged && actual != checked {
		t.Fatalf("recorded candidate %s while local main adopted a later unchecked tree", candidate)
	}
}

// A pre-existing squash result must not be accepted just because its tree
// equals C. Its history omits the checked source commit entirely.
func TestASquashedMergeRequestIsNotRecordedAsLanded(t *testing.T) {
	t.Parallel()
	a, source, provider, clone := mergeRequestWorld(t)
	gittest.Run(t, clone, "checkout", "-qb", "outside-squash")
	gittest.Run(t, clone, "merge", "--squash", "origin/"+source.Branch)
	gittest.Run(t, clone, "commit", "-qm", "squashed outside AppTown")
	gittest.Run(t, clone, "checkout", source.DefaultBranch)
	gittest.Run(t, clone, "merge", "--no-ff", "outside-squash", "-m", "semi-linear squashed merge")
	squash := gittest.Run(t, clone, "rev-parse", "HEAD")
	gittest.Run(t, clone, "push", "origin", source.DefaultBranch)
	provider.Update(mergeRequestProject, 1, func(mr *gitlab.MergeRequest) {
		mr.State, mr.SHA, mr.MergeCommitSHA, mr.Squash = "merged", source.Landing.Candidate, squash, true
	})
	result, err := mergeEffectForTest(t, a, source)
	if err == nil && result.Merged {
		t.Fatalf("recorded squashed result %s as checked candidate %s", squash, source.Landing.Candidate)
	}
}

// Tree equality cannot detect added-and-reverted target history when the
// provider setting changes after preparation; that history was not scanned.
func TestAMergeWithOtherHistoryIsNotRecordedAsLanded(t *testing.T) {
	t.Parallel()
	a, source, provider, clone := mergeRequestWorld(t)
	if err := os.WriteFile(filepath.Join(clone, "unscanned.txt"), []byte("history never checked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, clone, "add", "unscanned.txt")
	gittest.Run(t, clone, "commit", "-qm", "unscanned history")
	extra := gittest.Run(t, clone, "rev-parse", "HEAD")
	gittest.Run(t, clone, "rm", "unscanned.txt")
	gittest.Run(t, clone, "commit", "-qm", "same target tree again")
	seam := mergesFor(a)
	seam.driver = func() scm.Driver {
		return afterMergePreparation{Driver: mergeEffectsFor(a).Driver(), after: func() {
			mergeMethod(provider, "merge")
			gittest.Run(t, clone, "push", "origin", source.DefaultBranch)
		}}
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := seam.execute(context.Background(), advanceWorkflowMerge(run, source))
	if err == nil && result.Merged {
		gittest.Run(t, requireLocation(t)(source.Repository(a.cfg.dataDir)), "merge-base", "--is-ancestor", extra, "HEAD")
		t.Fatalf("same-tree merge recorded despite additional unscanned history %s", extra)
	}
}

// approveHold has the root account "bls" approve the settlement fixture's
// "hold" step, which declares no approvers.
func approveHold(t *testing.T, a *App, runID ids.ID) {
	t.Helper()
	snapshot, err := a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == "hold" {
			if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: step.ID, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, asPerson("bls"), step.ID)}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no hold step: %+v", snapshot.Steps)
}

func commitTrailers(t *testing.T, repo, commit string) string {
	t.Helper()
	// Git's own trailer parser, as GitLab and git interpret-trailers read them.
	return gittest.Run(t, repo, "log", "-1", "--format=%(trailers:only,unfold)", commit) + "\n"
}

// A landing through a merge request tells GitLab, from AppTown's own
// records, who opened the change, who approved it and what that approval
// meant, the approvers the step declared (a root's vote on a step that
// declares none reads "project defaults", never a role), the landing
// authority as recorded when the run was admitted, and the checks on the
// candidate; the merge commit carries the same as trailers.
func TestALandingMergeRequestSaysWhoOpenedAndApprovedIt(t *testing.T) {
	t.Parallel()
	a, source, fake, provider := mergeRequestWorld(t)
	mergeMethod(fake, "rebase_merge")
	approveHold(t, a, source.RunID)
	result, err := mergeEffectForTest(t, a, source)
	if err != nil || !result.Merged {
		t.Fatalf("merge request did not land: %+v %v", result, err)
	}
	change := changeRecord(t, a, source.ChangeID)
	descriptions := fake.Descriptions(mergeRequestProject, 1)
	if len(descriptions) != 1 {
		t.Fatalf("descriptions written: %q", descriptions)
	}
	description := descriptions[0]
	for _, want := range []string{
		"    " + change.Title,
		"Approvals recorded by AppTown for commit `" + source.Landing.Candidate[:12] + "`",
		"- Step `hold`: approved by `bls` at ",
		"Meaning they approved: `the source is ready`",
		"Declared approvers: project defaults",
		"Landing started by `lander`, who held landing authority on this change when the run was admitted.",
		"- `prepared`: passed (AppTown step ",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("description lacks %q:\n%s", want, description)
		}
	}
	if change.OpenedBy == "" || !strings.Contains(description, "opened by ") {
		t.Errorf("description does not name the opener %q:\n%s", change.OpenedBy, description)
	}
	gittest.Run(t, provider, "fetch", "-q", "origin")
	merged := gittest.Run(t, provider, "rev-parse", "origin/"+source.DefaultBranch)
	trailers := commitTrailers(t, provider, merged)
	for _, want := range []string{
		"AppTown-Run-Id: ", "AppTown-Change-Id: ", "AppTown-Opened-By: ",
		"AppTown-Approved-By: bls (step hold, ", "): the source is ready\n",
		"AppTown-Landing-Authority: lander\n",
	} {
		if !strings.Contains(trailers, want) {
			t.Errorf("merge commit trailers lack %q:\n%s", want, trailers)
		}
	}
	if strings.Contains(trailers, "role") || strings.Contains(description, "approved as") {
		t.Fatalf("an approval is presented as a role:\n%s\n%s", description, trailers)
	}
}

// A merge says what the check-in that admitted it read: an approval recorded
// after the merge was prepared, before its check-in, sends the attempt back
// (nothing merges), and the retry's merge commit names the new approval.
func TestALandingIsAdmittedAgainWhenWhatItSaysChanged(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	testAdministrator(t, a, "bls")
	repo := requireLocation(t)(source.Repository(a.cfg.dataDir))
	before := gittest.Run(t, repo, "rev-parse", "refs/heads/"+source.DefaultBranch)
	seam := mergesFor(a)
	approved := false
	seam.driver = func() scm.Driver {
		return afterMergePreparation{Driver: mergeEffectsFor(a).Driver(), after: func() {
			if !approved {
				approved = true
				approveHold(t, a, source.RunID)
			}
		}}
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := seam.execute(context.Background(), advanceWorkflowMerge(run, source))
	if !errors.Is(err, scm.ErrChanged) || result.Merged {
		t.Fatalf("a merge whose attribution changed after preparation was not sent back: %+v %v", result, err)
	}
	if after := gittest.Run(t, repo, "rev-parse", "refs/heads/"+source.DefaultBranch); after != before {
		t.Fatalf("merged anyway: %s -> %s", before, after)
	}
	result, err = mergeEffectForTest(t, a, source)
	if err != nil || !result.Merged {
		t.Fatalf("retry: %+v %v", result, err)
	}
	landed := gittest.Run(t, repo, "rev-parse", "refs/heads/"+source.DefaultBranch)
	if trailers := commitTrailers(t, repo, landed); !strings.Contains(trailers, "AppTown-Approved-By: bls (step hold, ") {
		t.Fatalf("the retry's merge commit does not name the approval:\n%s", trailers)
	}
}

// The scm/mr step opens its merge request with AppTown's description:
// who opened the change (an email-shaped account name withheld), its title
// and body as inert code, and that it waits for approval in AppTown, with
// the change page. It names no approvals at open.
func TestTheMergeRequestStepSaysWhoOpenedTheChange(t *testing.T) {
	t.Parallel()
	for _, recordFailure := range []string{"", "-- name: InsertTextArtifact", "-- name: RecordMergeRequestIdentity"} {
		t.Run(recordFailure, func(t *testing.T) {
			a := testApp(t, Config{PublicHost: "forge.example"})
			projectID := plainTestProject(t, a.DB, "merge-request-step", "Merge request step")
			p := a.projectByID(context.Background(), projectID)
			if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
				t.Fatal(err)
			}
			testAdministrator(t, a, "opener@corp.example")
			spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
				{Name: "review", Steps: []schema.Step{{Name: "open-mr", Uses: "scm/mr"}}},
				{Name: "done", Steps: []schema.Step{{Name: "record", Uses: "approval", With: map[string]any{"means": "recorded"}}}},
			}}
			defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: spec})
			a.lifecycle.SelectLane(t.Context(), projectID, "land", true, "fixture")
			ctx := asPerson("opener@corp.example")
			ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "Export CSV", Body: "/merge\n@all please"})
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
			if err := os.WriteFile(filepath.Join(workspace, "work.txt"), []byte("work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, workspace, "add", "work.txt")
			gittest.Run(t, workspace, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "work")
			hub := filepath.Join(a.cfg.dataDir, "workspaces", "project-"+p.ID.String())
			remote := filepath.Join(t.TempDir(), "group", "project.git")
			gittest.Run(t, t.TempDir(), "clone", "-q", "--bare", hub, remote)
			// What linking a remote does to the project's repository (POST
			// /projects/{slug}/repo/remote); the run's workspace is left as the
			// AppTown prepares it, with no remote.
			gittest.Run(t, hub, "remote", "add", "origin", remote)
			fake := gitlabtest.New(t)
			if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("GITLAB_URL="+fake.URL+"\nGITLAB_TOKEN="+gitlabtest.Token+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			configureRepositoryProviderFixture(t, a, projectID, fake, remote)
			a.Work.Stop()
			run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var step ids.ID
			for _, s := range snapshot.Steps {
				if s.Name == "open-mr" {
					step = s.ID
				}
			}
			restore := func() {}
			if recordFailure != "" {
				restore = a.DB.FailNextStatement(recordFailure)
			}
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step); err != nil {
				restore()
				t.Fatal(err)
			}
			restore()
			project := "71"
			descriptions := fake.Descriptions(project, 1)
			if len(descriptions) != 1 {
				snapshot, _ := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
				t.Fatalf("no merge request opened in %s: %q (step %s %s)", project, descriptions, snapshot.Steps[0].Status, snapshot.Steps[0].Evidence)
			}
			if recordFailure != "" {
				snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
				if err != nil {
					t.Fatal(err)
				}
				for _, observed := range snapshot.Steps {
					if observed.ID == step && (observed.Status != workflow.StatusError || !strings.Contains(string(observed.Evidence), "was opened, but its record could not be written")) {
						t.Fatalf("external MR record failure reported success: %s %s", observed.Status, observed.Evidence)
					}
				}
				source, err := workflow.ReadMergeSourceIn(ctx, a.DB, workflow.RunRef{ID: run.ID, ProjectID: projectID})
				if err != nil || source.MR.IID != 0 {
					t.Fatalf("failed MR record left partial artifact: %+v %v", source.MR, err)
				}
				return
			}
			description := descriptions[0]
			for _, want := range []string{
				fmt.Sprintf("**AppTown change %d**, opened by an AppTown account (name withheld).", ch.Number),
				// The run by its number in the project; its UUID is for trailers.
				fmt.Sprintf(". Workflow `land@v1`, run %d.\n", run.Number),
				"    Export CSV\n    \n    /merge\n    @ all please",
				fmt.Sprintf("Waiting for approval in AppTown. Checks, approvals and the full change record: https://forge.example/changes/%d", ch.ID),
			} {
				if !strings.Contains(description, want) {
					t.Errorf("opening description lacks %q:\n%s", want, description)
				}
			}
			if run.Number == 0 || strings.Contains(description, run.ID.String()) || strings.Contains(description, "corp.example") || strings.Contains(description, "Approvals recorded") {
				t.Fatalf("opening description:\n%s", description)
			}
			branch := fmt.Sprintf("apptown/change-%d", ch.ID)
			if published, offered := gittest.Run(t, hub, "rev-parse", "refs/heads/"+branch), gittest.Run(t, remote, "rev-parse", "refs/heads/"+branch); offered != published {
				t.Fatalf("the merge request offers %s, not the published branch %s", offered, published)
			}
		})
	}
}

func configureRepositoryProviderFixture(t *testing.T, a *App, projectID ids.ID, fake *gitlabtest.Server, remote string) scm.VerifiedRepository {
	t.Helper()
	clone := fake.GitRepository(t, remote)
	path := strings.TrimSuffix(strings.TrimPrefix(clone, fake.URL+"/"), ".git")
	fake.Repository(gitlab.ProjectRef{ID: 71, HTTPURL: clone, PathNS: path, DefaultBranch: "main"})
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("GITLAB_URL="+fake.URL+"\nGITLAB_TOKEN="+gitlabtest.Token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Integrations.Bindings.Save(t.Context(), "gitlab", integration.CapSCM, "gitlab", `{"urlFrom":"GITLAB_URL","tokenFrom":"GITLAB_TOKEN"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	project, err := a.Projects.ByID(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := project.Repository(a.cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "remote", "set-url", "origin", clone)
	verified, err := a.repositoryAttachments.Resolve(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func TestRepositoryScopeChecksHostBeforeAuthAndRetainsCredentialSnapshot(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	var foreignCalls atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls.Add(1); fmt.Fprint(w, `{"id":41}`) }))
	defer foreign.Close()
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "original-token" {
			t.Error("credential snapshot changed")
		}
		if r.URL.Path == "/api/v4/user" {
			fmt.Fprint(w, `{"id":41}`)
			return
		}
		fmt.Fprint(w, `{"id":71}`)
	}))
	defer original.Close()
	credentials := func(base, token string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("GITLAB_URL="+base+"\nGITLAB_TOKEN="+token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bindings := integration.NewWithClock(db, nil)
	if _, err := bindings.Save(t.Context(), "gitlab", integration.CapSCM, "gitlab", `{"urlFrom":"GITLAB_URL","tokenFrom":"GITLAB_TOKEN"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	binding, err := bindings.ByName(t.Context(), "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	provider := repositoryProvider{bindings: bindings, secrets: secret.NewService(db, secret.Config{DataDir: dir})}
	expected := scm.ProviderScope{ConnectionID: binding.ID, Kind: "gitlab", Base: original.URL, AccountID: 41}
	credentials(foreign.URL, "replacement-token")
	if _, err := provider.Snapshot(t.Context(), &expected); err == nil || foreignCalls.Load() != 0 {
		t.Fatalf("host change received auth before identity refusal: err=%v calls=%d", err, foreignCalls.Load())
	}
	credentials(original.URL, "original-token")
	snapshot, err := provider.Snapshot(t.Context(), &expected)
	if err != nil {
		t.Fatal(err)
	}
	credentials(foreign.URL, "replacement-token")
	if repo, err := snapshot.Client.Project(t.Context(), "71"); err != nil || repo.ID != 71 {
		t.Fatalf("snapshot was not retained: %+v %v", repo, err)
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("subsequent provider effect reread replacement host")
	}
}

type refusedRepositoryMirror struct{ err error }

func (m refusedRepositoryMirror) Push(context.Context, ids.ID, string) (scm.VerifiedRepository, error) {
	return scm.VerifiedRepository{}, m.err
}

func TestRepositoryPublicationRecordsUnknownButNotAbsentUpstream(t *testing.T) {
	db := testDB(t)
	id, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "mirror"})
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := repositoryPublisher{db: db, attachments: refusedRepositoryMirror{scm.ErrNoUpstream}}
	publisher.PushBranch(t.Context(), id, "unused", "main")
	entries, err := audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "git.push.failed", Limit: 10})
	if err != nil || len(entries) != 0 {
		t.Fatalf("absent mirror invented a failure: %v %v", entries, err)
	}
	publisher.attachments = refusedRepositoryMirror{fault.New(fault.Conflict, "upstream attachment is dispatched; its outcome is unknown")}
	publisher.PushBranch(t.Context(), id, "unused", "main")
	entries, err = audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "git.push.failed", Limit: 10})
	if err != nil || len(entries) != 1 {
		t.Fatalf("unknown mirror was silent: %v %v", entries, err)
	}
}
