package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

func stepStatusEv(t *testing.T, a *App, id ids.ID) (workflow.StepState, string) {
	t.Helper()
	step, err := workflow.ReadStepIn(context.Background(), a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return step.Status, string(step.Evidence)
}

// The land preflight only reads: a branch that already contains main is
// pinned and passes; a branch behind main fails early with the reason,
// merging nothing and moving nothing, and ends the landing with the change
// back in progress; and the hub refuses a push (can pull, can't push).
func TestReconcilePreflight(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	slug := "recon"
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: slug, Name: "R", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.ID
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	hub := a.projectRepoPath(slug)
	_ = os.WriteFile(filepath.Join(hub, "app.txt"), []byte("base\n"), 0o644)
	stageAll(hub)
	if out, err := gitexec.Run(t.Context(), hub, "commit", "-m", "base"); err != nil {
		t.Fatalf("base commit: %s", out)
	}

	steps := []schema.Step{{Name: "already-current", Uses: "reconcile"}, {Name: "behind-main", Uses: "reconcile"}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: steps}}}})
	a.Work.Stop() // each real reconcile execution is driven at the intended Git boundary below
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID))
	if err := os.WriteFile(filepath.Join(folder, "change.txt"), []byte("the change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, folder, "add", "change.txt")
	gittest.Run(t, folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change")
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("declared reconcile steps: %+v %v", snapshot, err)
	}
	stepID := func(name string) ids.ID {
		for _, step := range snapshot.Steps {
			if step.Name == name {
				return step.ID
			}
		}
		t.Fatalf("missing reconcile step %q", name)
		return ids.Nil
	}

	// A — the branch contains main: pinned, and it passes.
	s := stepID("already-current")
	a.Work.ExecuteStep(a.asEngine(context.Background()), s)
	if st, ev := stepStatusEv(t, a, s); st != workflow.StatusPass {
		t.Fatalf("a branch containing main must pass, got %s: %s", st, ev)
	}

	// B — main moves on: the branch is behind it, and the landing fails here,
	// merging nothing and moving no branch.
	_ = os.WriteFile(filepath.Join(hub, "app.txt"), []byte("base\nmain-added\n"), 0o644)
	stageAll(hub)
	_, _ = gitexec.Run(ctx, hub, "commit", "-m", "main moves")
	branchBefore := gittest.Run(t, hub, "rev-parse", "refs/heads/"+change.Branch)
	mainBefore := gittest.Run(t, hub, "rev-parse", "refs/heads/main")
	s = stepID("behind-main")
	a.Work.ExecuteStep(a.asEngine(context.Background()), s)
	st, ev := stepStatusEv(t, a, s)
	if st != workflow.StatusFail || !strings.Contains(ev, "behind main") {
		t.Fatalf("a branch behind main must fail with the reason, got %s: %s", st, ev)
	}
	if gittest.Run(t, hub, "rev-parse", "refs/heads/"+change.Branch) != branchBefore || gittest.Run(t, hub, "rev-parse", "refs/heads/main") != mainBefore {
		t.Fatal("the preflight moved a branch")
	}
	// The landing ends there, and its change is back in progress: its chat
	// can run again to catch up, and the change can land again.
	ended, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || ended.Run.Status != workflow.RunAbandoned {
		t.Fatalf("a landing behind main must end at the preflight: %+v %v", ended.Run, err)
	}
	// Why it ended, and the catch-up, are read with it: apptown wait -c and
	// the change's chat say this rather than "a person's move".
	if !strings.Contains(ended.Ending, change.Branch+" is behind main; behind-main (fail): behind main") || !strings.Contains(ended.Ending, `git fetch "$APPTOWN_MAIN_REMOTE" main`) {
		t.Fatalf("the ended landing must say why and how to catch up: %q", ended.Ending)
	}
	back, err := a.Work.Change(ctx, change.ID)
	if err != nil || back.State != workflow.ChangeInProgress {
		t.Fatalf("the change of an ended landing must be back in progress: %+v %v", back, err)
	}

	// Enforcement: the hub refuses a push (can pull, can't push).
	if out, err := gitexec.Run(ctx, folder, "push", hub, "HEAD:refs/heads/sneaky"); err == nil {
		t.Fatalf("the hub must refuse a push, but it succeeded: %s", out)
	}
}

// A change's chat is told once, with the reason and the catch-up, when its
// landing ends without landing (here main moved under it), and nothing
// while the landing goes well: change 61's chat, told nothing, went on
// saying its landing awaited approval after the landing had ended.
func TestAChangesChatIsToldOnceWhenItsLandingEnds(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	a := w.a
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Runners = a.workDependencies().Runners })
	// The chat requests the landing, so its workflow waits for a person
	// before it merges, as every landing an agent can start must.
	steps := []schema.Step{{Name: "already-current", Uses: "reconcile"}, {Name: "catch-up-with-main", Uses: "reconcile"}, {Name: "accept", Uses: "approval", With: map[string]any{"means": "land it"}}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: steps}}}})
	a.Work.Stop() // each reconcile is driven at the intended Git boundary below
	id, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open the chat: %d %s", code, body)
	}
	s, err := a.Console.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	notices := func() (n int, text string) {
		for _, m := range s.Messages() {
			if strings.Contains(m.Text, "[forge landing]") {
				n, text = n+1, m.Text
			}
		}
		return n, text
	}
	if err := os.WriteFile(filepath.Join(w.folder, "change.txt"), []byte("the change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "add", "change.txt")
	gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change")
	run, err := a.Work.RequestLanding(conversation.Context(context.Background(), s), w.change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(asPerson("owner"), run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 3 {
		t.Fatalf("declared reconcile steps: %+v %v", snapshot, err)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), snapshot.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if st, ev := stepStatusEv(t, a, snapshot.Steps[0].ID); st != workflow.StatusPass {
		t.Fatalf("a branch containing main must pass, got %s: %s", st, ev)
	}
	if n, _ := notices(); n != 0 {
		t.Fatalf("a landing going well told the chat %d times", n)
	}

	hub := a.projectRepoPath("game")
	_ = os.WriteFile(filepath.Join(hub, "main.txt"), []byte("main moves\n"), 0o644)
	stageAll(hub)
	if out, err := gitexec.Run(t.Context(), hub, "commit", "-m", "main moves"); err != nil {
		t.Fatalf("main moves: %s", out)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), snapshot.Steps[1].ID); err != nil {
		t.Fatal(err)
	}
	back, err := a.Work.Change(asPerson("owner"), w.change.ID)
	if err != nil || back.State != workflow.ChangeInProgress {
		t.Fatalf("the change of an ended landing must be back in progress: %+v %v", back, err)
	}
	runQueuedJobForTest(t, a, workflowAbandonmentJob, fmt.Sprintf("run/%d/narrative", run.ID))
	n, text := notices()
	if n != 1 || !strings.Contains(text, fmt.Sprintf("landing (run %d) ended", run.ID)) || !strings.Contains(text, "behind main") || !strings.Contains(text, `git fetch "$APPTOWN_MAIN_REMOTE" main`) {
		t.Fatalf("the chat must be told once why its landing ended and how to catch up, got %d: %q", n, text)
	}
}
