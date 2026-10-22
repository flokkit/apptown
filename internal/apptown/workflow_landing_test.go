package apptown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAutoLandRequiresLandingLane(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "p")
	change := createReadTestChange(t, a, pid, "t")
	installFakeGit(t, a, nil)
	workSpec := schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: workSpec})
	running, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: change, Workflow: "work"})
	if err != nil {
		t.Fatal(err)
	}
	runID := running.ID

	run, _ := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err := a.startAcceptedLanding(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	} // no landing workflow installed
	if n := workEventCount(t, a, func(e workflow.Event) bool { return strings.HasPrefix(e.Kind, "landing.") }); n != 0 {
		t.Fatalf("no landing lane must mean no landing events, got %d", n)
	}

}

// The landing machinery against REAL git: init, change worktrees, the
// sweep-at-land commit, the merge, and the conflict answer. These are the
// paths a landing lives or dies on (debt #3 named them live-fire-only).
func TestGitLandingMachinery(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	plainTestProject(t, a.DB, "p", "P")
	p, _ := a.Projects.BySlug(context.Background(), "p")

	if err := a.lifecycle.InitializeRepository(t.Context(), &p, "", ""); err != nil {
		t.Fatal(err)
	}
	repo := a.projectRepoPath("p")
	if !gitexec.IsRepo(repo) {
		t.Fatal("init did not produce a repo")
	}
	if br, _ := gitexec.Run(t.Context(), repo, "branch", "--show-current"); br != "main" {
		t.Fatalf("default branch: %q", br)
	}
	gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	// The CURRENT contract form must be whitelisted (change-99 lesson; the
	// compose→app.yaml pivot re-broke this and eight-ball shipped undeployed).
	if !strings.Contains(string(gi), "!.apptown/app.yaml") {
		t.Fatalf("the contract must stay versioned: %s", gi)
	}

	// A change gets a folder on its own branch; what its worker committed
	// there is published when the landing starts.
	ch := engineLandingChange(t, a, &p)
	chID := ch.ID
	if err := prepareChangeWorkspaceForTest(a, &p, ch); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	if err := os.WriteFile(filepath.Join(wt, "hello.txt"), []byte("from the change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, wt, "add", "hello.txt")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "hello from the change")
	run := engineLandingRun(t, a, &p, ch)

	// The merge: a run on the change's branch lands it on main, no-ff,
	// with run trailers.
	merged, conflicts, _ := mergeRunForTest(t, a, run)
	if !merged || len(conflicts) != 0 {
		t.Fatalf("merge failed: %v %v", merged, conflicts)
	}
	if !branchMergedForTest(t, a, run) {
		t.Fatal("runBranchMerged must see the landed branch")
	}
	// The title names the run by its number in the project; the trailer
	// carries its UUID.
	if out, _ := gitexec.Run(t.Context(), repo, "log", "-1", "--format=%B"); run.Number == 0 || !strings.HasPrefix(out, fmt.Sprintf("apptown: complete %s run %d\n", run.WorkflowName, run.Number)) || !strings.Contains(out, "AppTown-Run-Id: "+run.ID.String()+"\n") {
		t.Fatalf("merge commit must name the run and carry its trailer: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "hello.txt")); err != nil {
		t.Fatal("the committed work did not land on main")
	}

	// Merging twice is idempotent — "already merged" is a pass, not a
	// second merge commit.
	before, _ := gitexec.Run(t.Context(), repo, "rev-parse", "HEAD")
	merged, _, _ = mergeRunForTest(t, a, run)
	after, _ := gitexec.Run(t.Context(), repo, "rev-parse", "HEAD")
	if !merged || before != after {
		t.Fatal("re-merge must be a no-op")
	}
}

func TestALandingThatDidNotCatchUpIsRefusedBeforeMerging(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	plainTestProject(t, a.DB, "p", "P")
	p, _ := a.Projects.BySlug(context.Background(), "p")
	if err := a.lifecycle.InitializeRepository(t.Context(), &p, "", ""); err != nil {
		t.Fatal(err)
	}
	repo := a.projectRepoPath("p")
	ch := engineLandingChange(t, a, &p)
	if err := prepareChangeWorkspaceForTest(a, &p, ch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("landed elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageAll(repo)
	gittest.Run(t, repo, "commit", "-qm", "main moved")
	before := gittest.Run(t, repo, "rev-parse", "HEAD")
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(wt, "change.txt"), []byte("the change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, wt, "add", "change.txt")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change")
	run := engineLandingRun(t, a, &p, ch)
	result, err := mergeEffectForTest(t, a, mergeSourceForTest(t, a, run))
	if err == nil || result.Merged || !strings.Contains(err.Error(), "catch-up") {
		t.Fatalf("an uncaught-up landing merged or was not refused with the reason: %+v %v", result, err)
	}
	if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatalf("the refused landing changed main: %s, was %s", got, before)
	}
}

// A landing lands what the change's worker committed, never uncommitted
// files: those stay in the folder, and the landing's record names them. A
// change with nothing committed is refused, never landed empty (star-hop
// shipped an EMPTY change when uncommitted work was left behind).
func TestALandingLeavesUncommittedWorkAndSaysSo(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p := engineProject(t, a, "p")
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	landingYAML := strings.Replace(lifecycleYAML, "name: flow", "name: land", 1)
	landingYAML = strings.Replace(landingYAML, "kind: lifecycle", "kind: lifecycle\n  role: landing", 1)
	seedLibraryWorkflow(t, a, idstest.N(1), "land", landingYAML)
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(wt, "the-work.txt"), []byte("hand edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := a.asAppTown(context.Background(), p.ID, "test")
	if _, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"}); err == nil || !strings.Contains(err.Error(), "nothing to land") {
		t.Fatalf("a landing with nothing committed: %v", err)
	}
	gittest.Run(t, wt, "add", "the-work.txt")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the work")
	if err := os.WriteFile(filepath.Join(wt, "later.txt"), []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	if status := gittest.Run(t, wt, "status", "--porcelain"); !strings.Contains(status, "later.txt") {
		t.Fatalf("the uncommitted file left the folder: %q", status)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.TraceView)
	if err != nil {
		t.Fatal(err)
	}
	named := false
	for _, event := range snapshot.Events {
		named = named || (event.Kind == "landing.uncommitted" && strings.Contains(string(event.Payload), "later.txt"))
	}
	if !named {
		t.Fatal("the landing's record does not name the uncommitted file")
	}
}

// Out-of-band edits: a dirty project working tree is an integrity event —
// the merge refuses with the drift named (never a fake "conflict"), the
// audit log records what was found, and only an explicit admin decision
// (adopt or discard) clears it.
func TestOutOfBandEditsBlockMerge(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p := engineProject(t, a, "p")
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	repo := a.projectRepoPath("p")

	ch := engineLandingChange(t, a, p)
	chID := ch.ID
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	if err := os.WriteFile(filepath.Join(wt, "game.js"), []byte("legit change work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, wt, "add", "game.js")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "legit change work")
	run := engineLandingRun(t, a, p, ch)

	// Someone edits main's working copy directly — no authorized path does
	// this; every sanctioned writer commits.
	if err := os.WriteFile(filepath.Join(repo, "tampered.txt"), []byte("who wrote this?\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, conflicts, drift := mergeRunForTest(t, a, run)
	if merged {
		t.Fatal("must not merge over out-of-band edits")
	}
	if len(conflicts) != 0 {
		t.Fatalf("drift must not masquerade as a conflict: %v", conflicts)
	}
	if len(drift) != 1 || drift[0] != "tampered.txt" {
		t.Fatalf("drift must name the files: %v", drift)
	}
	// AppTown's own runtime leftovers under .apptown/ are not drift; a
	// touched contract file is.
	repoDir := a.projectRepoPath("p")
	_ = os.MkdirAll(filepath.Join(repoDir, ".apptown", "sessions"), 0o755)
	_ = os.WriteFile(filepath.Join(repoDir, ".apptown", "sessions", "old.jsonl"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(repoDir, ".apptown", "steering.md"), []byte("x"), 0o644)
	if d, err := scm.Drift(context.Background(), a.processRunner, repoDir); err != nil || len(d) != 1 || d[0] != "tampered.txt" {
		t.Fatalf("runtime files under .apptown must not count as drift: %v", d)
	}
	ev := workflow.MergeBlockEvidence(conflicts, drift)
	if !strings.Contains(ev.Reason, "did not come through AppTown") {
		t.Fatalf("the reason must say what this is: %v", ev.Reason)
	}
	if audits := auditCount(t, a.DB, "integrity.out-of-band-edits"); audits != 1 {
		t.Fatalf("detection must be audited, got %d rows", audits)
	}

	// Discard restores the clean tree; the merge then lands.
	if _, err := gitexec.Run(t.Context(), repo, "checkout", "--", "."); err != nil {
		t.Fatal(err)
	}
	_, _ = gitexec.Run(t.Context(), repo, "clean", "-fd", "-e", ".apptown")
	if d, err := scm.Drift(context.Background(), a.processRunner, repo); err != nil || len(d) != 0 {
		t.Fatalf("tree must be clean after discard: %v", d)
	}
	if merged, _, _ := mergeRunForTest(t, a, run); !merged {
		t.Fatal("clean tree must merge")
	}
}
