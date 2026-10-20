package forge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

const addsContract = `app:
  web:
    build: .
    ports: ["8080"]
    public: true
needs:
  postgres:
    version: "16"
`

const addsSpec = `[project]
name = "shop"
revision = "1.0"

[profiles.default]
STRIPE_KEY = { description = "payments" }
`

// addsFixture is a change whose working run ends in an acceptance, with a
// landing whose approval an acceptance may count for, and a way to commit
// and publish files on the change's branch.
type addsFixture struct {
	a        *App
	work     *workflow.Run
	acceptID ids.ID
	commit   func(files map[string]string) string
}

func newAddsFixture(t *testing.T, slug string) addsFixture {
	t.Helper()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	pid := plainTestProject(t, a.DB, slug, slug)
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
	f := addsFixture{a: a, work: work}
	f.commit = func(files map[string]string) string {
		t.Helper()
		for path, body := range files {
			full := filepath.Join(ws, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, ws, "add", path)
		}
		gittest.Run(t, ws, "commit", "-qm", "declarations")
		if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: pid, Repository: source.Repository(a.cfg.dataDir), Branch: source.Branch, From: ws, Grant: scm.Grant{Holder: scm.RunHolder(work.ID)}}); err != nil {
			t.Fatal(err)
		}
		return gittest.Run(t, ws, "rev-parse", "HEAD")
	}
	snap, err := a.Work.Snapshot(asPerson("bls"), work.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == "accept" {
			f.acceptID = s.ID
		}
	}
	return f
}

// votesOn reads an approval's recorded votes through Work's own read.
func votesOn(t *testing.T, a *App, runID ids.ID, step string) []workflow.ApprovalVote {
	t.Helper()
	snap, err := a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == step {
			evidence, err := workflow.ReadApprovalEvidence(s.Evidence)
			if err != nil {
				t.Fatal(err)
			}
			return evidence.Decisions
		}
	}
	t.Fatalf("run %d has no step %s", runID, step)
	return nil
}

func lineTexts(a *workflow.ChangeAdds) []string {
	var out []string
	for _, l := range a.Lines {
		out = append(out, l.Text)
	}
	return out
}

// A rejection needs no list: it accepts nothing into the app.
func TestARejectionNeedsNoList(t *testing.T) {
	t.Parallel()
	f := newAddsFixture(t, "adds-reject")
	f.commit(map[string]string{".forge/app.yaml": addsContract})
	if _, err := f.a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: f.acceptID, Decision: workflow.VoteReject, Comment: "not yet"}); err != nil {
		t.Fatal(err)
	}
	if votes := votesOn(t, f.a, f.work.ID, "accept"); len(votes) != 1 || votes[0].Adds != nil {
		t.Fatalf("a rejection recorded a list: %+v", votes)
	}
}

// Every client approves through the API's one approve path: one that does
// not send the list is refused there, in the forge's words.
func TestTheApproveAPIRefusesAVoteWithoutTheList(t *testing.T) {
	t.Parallel()
	f := newAddsFixture(t, "adds-api")
	f.commit(map[string]string{".forge/app.yaml": addsContract})
	h := f.a.Handler()
	w := authzCall(h, "bls", "POST", "/api/v1/steps/"+f.acceptID.String()+"/approve", `{"decision":"approve","alsoApproves":["merge the accepted work"]}`)
	if w.Code != 400 || w.Header().Get("X-AppTown-Deny-Code") != "adds-not-shown" {
		t.Fatalf("approved over the API without the list: %d %s", w.Code, w.Body.String())
	}
	if votes := votesOn(t, f.a, f.work.ID, "accept"); len(votes) != 0 {
		t.Fatalf("a refused vote was recorded: %+v", votes)
	}
}

// landAccepted starts the landing of an accepted change, runs its checks
// and advances it to the approval the acceptance may count for.
func landAccepted(t *testing.T, f addsFixture) ids.ID {
	t.Helper()
	a := f.a
	if _, err := advanceAs(a, f.work.ID, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.AdvanceAcceptedLanding(context.Background(), f.work.ID, alwaysCanLand{}); !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("creating landing: %v", err)
	}
	runs, err := a.Work.ChangeRuns(asPerson("bls"), f.work.ChangeID, workflow.ChangeRuns{RootsOnly: true, Limit: 1})
	if err != nil || len(runs) != 1 || runs[0].ID == f.work.ID {
		t.Fatalf("the change's newest run is not the landing: %v %v", runs, err)
	}
	landing, err := workflow.ReadRunIn(context.Background(), a.DB, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if check := reviewExecute(t, a, landing, "tests"); check.Status != workflow.StatusPass {
		t.Fatalf("tests: %s %s", check.Status, check.Evidence)
	}
	if _, err := a.Work.AdvanceAcceptedLanding(context.Background(), f.work.ID, alwaysCanLand{}); err != nil {
		t.Fatal(err)
	}
	if _, err := advanceAs(a, landing.ID, "system"); err != nil {
		t.Fatal(err)
	}
	return landing.ID
}

// An acceptance counts for the landing's approval of the same commit when
// the landing reads, through the forge's own reader, the same list the
// accepter was shown; the carried vote records the accepter's list.
// Workflow's TestAnAcceptanceCarriesOnlyWithTheListItWasShown covers a
// landing that reads another list.
func TestAnAcceptanceCarriesWithTheListItWasShown(t *testing.T) {
	t.Parallel()
	f := newAddsFixture(t, "carry-same-list")
	f.commit(map[string]string{".forge/app.yaml": addsContract, "secretspec.toml": addsSpec})
	ctx := asPerson("bls")
	q, err := f.a.Work.ApprovalQuestion(ctx, f.acceptID)
	if err != nil || q.Adds == nil {
		t.Fatalf("question: %+v %v", q, err)
	}
	if _, err := f.a.Work.Approve(ctx, workflow.Approval{StepID: f.acceptID, Decision: workflow.VoteApprove, Shown: q.LandingMeans, Adds: q.Adds.Digest}); err != nil {
		t.Fatal(err)
	}
	landingID := landAccepted(t, f)
	snap, err := f.a.Work.Snapshot(ctx, landingID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var approve *workflow.Step
	for _, s := range snap.Steps {
		if s.Name == "approve" {
			approve = s
		}
	}
	if approve == nil || approve.Status != workflow.StatusPass {
		t.Fatalf("an acceptance with the landing's own list did not carry: %+v", approve)
	}
	votes := votesOn(t, f.a, landingID, "approve")
	if len(votes) != 1 || votes[0].Adds == nil || votes[0].Adds.Digest != q.Adds.Digest {
		t.Fatalf("the carried vote does not record the accepter's list: %+v", votes)
	}
}

// The change's reads carry what its waiting approval's commit adds: the API
// change read the web page and apptown show use, and the MCP change read an
// agent uses to say what a person will be asked to accept.
func TestChangeReadsCarryWhatTheWaitingApprovalAdds(t *testing.T) {
	t.Parallel()
	f := newAddsFixture(t, "adds-reads")
	f.commit(map[string]string{".forge/app.yaml": addsContract, "secretspec.toml": addsSpec})
	w := authzCall(f.a.Handler(), "bls", "GET", "/api/v1/changes/"+f.work.ChangeID.String(), "")
	if w.Code != 200 {
		t.Fatalf("change read: %d %s", w.Code, w.Body.String())
	}
	var view struct {
		Approval *struct {
			StepID ids.ID               `json:"stepId"`
			Adds   *workflow.ChangeAdds `json:"adds"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Approval == nil || view.Approval.StepID != f.acceptID || view.Approval.Adds == nil || !slices.Contains(lineTexts(view.Approval.Adds), "Secrets: STRIPE_KEY") {
		t.Fatalf("API change read: %s", w.Body.String())
	}
	tok := mcp_token(t, f.a, "laptop", "bls", "member")
	res := mcp_call(t, f.a, tok, "get_change", map[string]any{"id": f.work.ChangeID})
	waiting, _ := res["structuredContent"].(map[string]any)["waiting"].(map[string]any)
	adds, _ := waiting["adds"].(map[string]any)
	if adds == nil || adds["digest"] != view.Approval.Adds.Digest {
		t.Fatalf("MCP get_change waiting: %v", waiting)
	}
}

// breakRef makes a repository's ref for the branch unreadable.
func breakRef(t *testing.T, repo, branch string) {
	t.Helper()
	path := gittest.Run(t, repo, "rev-parse", "--git-path", "refs/heads/"+branch)
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	// Repository damage has no owner operation; the test writes the ref
	// file itself.
	if err := os.WriteFile(path, []byte("not-a-valid-ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A commit an acceptance would accept that cannot be read is not "no
// commit": the question fails and an approving vote is refused, never
// recorded without a list; and an unreadable branch in the change's folder
// does not fall back to the published one.
func TestAnUnreadableAcceptedCommitRefusesTheVote(t *testing.T) {
	t.Parallel()
	for _, both := range []bool{false, true} {
		name := "folder only"
		if both {
			name = "folder and repository"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newAddsFixture(t, strings.ReplaceAll("unreadable "+name, " ", "-"))
			f.commit(map[string]string{".forge/app.yaml": addsContract})
			source := workflowSourceForTest(t, f.a, f.work)
			breakRef(t, source.Workspace(f.a.cfg.dataDir), source.Branch)
			if both {
				breakRef(t, source.Repository(f.a.cfg.dataDir), source.Branch)
			}
			ctx := asPerson("bls")
			if q, err := f.a.Work.ApprovalQuestion(ctx, f.acceptID); err == nil {
				t.Fatalf("the question read past an unreadable branch: %+v", q.Adds)
			}
			if _, err := f.a.Work.Approve(ctx, workflow.Approval{StepID: f.acceptID, Decision: workflow.VoteApprove}); err == nil {
				t.Fatal("approved a commit that could not be read")
			}
			if votes := votesOn(t, f.a, f.work.ID, "accept"); len(votes) != 0 {
				t.Fatalf("a vote was recorded: %+v", votes)
			}
		})
	}
}
