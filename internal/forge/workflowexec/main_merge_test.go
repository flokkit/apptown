package workflowexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// mainMergeWorld is a change's run admitted through Work, holding the
// change's branch, in the fixture's hub, with an agent step.
type mainMergeWorld struct {
	f      *agentOnWorker
	work   *workflow.Service
	ctx    context.Context
	run    *workflow.Run
	step   *workflow.Step
	source workflow.Source
}

func newMainMergeWorld(t *testing.T) *mainMergeWorld {
	t.Helper()
	f := agentOnWorkerFixture(t, nil)
	db := f.exec.db
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "maker"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "maker", Via: "session"}, Verified: true})
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
		{Name: "rework", Uses: AgentKind, With: map[string]any{"prompt": "rework"}}}}}}
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority: access.Checked{Authority: directory, Projects: project.New(db, directory)},
		DataDir:   f.dir, Repositories: &scm.RepositoryLocks{},
		Resolve: func(_ context.Context, _ workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "rework", Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
		Automatic: []string{AgentKind},
		Runners:   map[string]workflow.Runner{AgentKind: func(*workflow.Execution) {}},
	})
	t.Cleanup(work.Stop)
	change, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: f.call.run.ProjectID, Title: "Rework"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "rework"})
	if err != nil {
		t.Fatal(err)
	}
	work.Wait()
	steps, err := work.RunSteps(ctx, run.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("the run's steps: %+v %v", steps, err)
	}
	source, err := workflow.ReadSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's project has no repository on record, so Work prepares
	// no folder and its run takes no branch; the run's source is the
	// fixture's hub on main, and the run takes the change's branch as a
	// repository-backed run would at its start.
	source.DefaultBranch = "main"
	if _, err := database.Transact(ctx, db, func(tx *database.Tx) (scm.Authority, error) {
		return workflow.TakeChangeIn(ctx, tx, run.ProjectID, source.Branch, scm.RunHolder(run.ID), nil, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	w := &mainMergeWorld{f: f, work: work, ctx: ctx, run: run, step: steps[0], source: source}
	w.git("branch", source.Branch, "main")
	return w
}

func (w *mainMergeWorld) git(args ...string) string {
	cmd := gittest.Command(args...)
	cmd.Dir = w.f.hub
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	return strings.TrimSpace(string(out))
}

// commit commits files on branch in the hub and returns the commit.
func (w *mainMergeWorld) commit(t *testing.T, branch string, files map[string]string) string {
	t.Helper()
	w.git("checkout", "-q", branch)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(w.f.hub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.git("add", "-A")
	w.git("commit", "-q", "-m", "to "+branch)
	tip := w.git("rev-parse", "HEAD")
	w.git("checkout", "-q", "main")
	return tip
}

// generation is the generation the run holds its change's branch under.
func (w *mainMergeWorld) generation(t *testing.T) int64 {
	t.Helper()
	authority, err := scm.ReadAuthorityIn(w.ctx, w.f.exec.db, w.run.ProjectID, w.source.Branch)
	if err != nil || authority.Holder != scm.RunHolder(w.run.ID) {
		t.Fatalf("the run does not hold its branch: %+v %v", authority, err)
	}
	return authority.Generation
}

func (w *mainMergeWorld) events(t *testing.T) []workflow.Event {
	t.Helper()
	snapshot, err := w.work.Snapshot(w.ctx, w.run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Events
}

func (w *mainMergeWorld) conflictNotices(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range w.events(t) {
		if e.Kind == repositoryedit.MainConflictNotice {
			var payload map[string]any
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			out = append(out, payload)
		}
	}
	return out
}

// Before a change's step, the forge merges main in when the branch is
// behind it, the merge is clean and the run holds the branch under the
// step's attempt: an ordinary merge, the branch then main, by the forge,
// nothing published yet. A branch that contains main needs none, and a
// branch the run does not hold under that attempt is left alone.
func TestTheForgeMergesMainIntoAChangeBeforeAStepWhenItCan(t *testing.T) {
	w := newMainMergeWorld(t)
	gen := w.generation(t)
	tip := w.commit(t, w.source.Branch, map[string]string{"change.txt": "the change\n"})

	if got := w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, gen, w.source, tip); got.Commit != "" || got.Main != "" {
		t.Fatalf("a branch that contains main was given a merge: %+v", got)
	}

	main := w.commit(t, "main", map[string]string{"later.txt": "main moved\n"})
	refs := w.git("for-each-ref")
	for name, held := range map[string]int64{"a newer attempt": gen + 1, "an attempt admitted before": gen - 1} {
		if got := w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, held, w.source, tip); got.Commit != "" {
			t.Fatalf("%s was given a merge: %+v", name, got)
		}
	}
	got := w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, gen, w.source, tip)
	if got.Commit == "" || got.Main != main || len(got.Conflicts) != 0 {
		t.Fatalf("a branch behind main was given no merge: %+v", got)
	}
	if parents := strings.Fields(w.git("rev-list", "--parents", "-n", "1", got.Commit)); !slices.Equal(parents, []string{got.Commit, tip, main}) {
		t.Fatalf("the merge's parents: %v", parents)
	}
	if subject := w.git("log", "-1", "--format=%cn %s", got.Commit); subject != "forge "+repositoryedit.MergeTitle("main", w.source.Branch) {
		t.Fatalf("the merge is %q", subject)
	}
	if after := w.git("for-each-ref"); after != refs {
		t.Fatalf("making the merge moved a ref before it was published:\n%s\nthen\n%s", refs, after)
	}
	if n := len(w.conflictNotices(t)); n != 0 {
		t.Fatalf("a clean merge was reported as a conflict %d times", n)
	}

	// The branch handed to a chat is the chat's to catch up.
	if _, err := database.Transact(context.Background(), w.f.exec.db, func(tx *database.Tx) (scm.Authority, error) {
		return scm.AdvanceAuthorityIn(w.ctx, tx, w.run.ProjectID, w.source.Branch, scm.ChatHolder("chat-1"))
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, w.generationAfterHandover(t), w.source, tip); got.Commit != "" {
		t.Fatalf("a branch a chat holds was given a merge: %+v", got)
	}
}

func (w *mainMergeWorld) generationAfterHandover(t *testing.T) int64 {
	t.Helper()
	authority, err := scm.ReadAuthorityIn(w.ctx, w.f.exec.db, w.run.ProjectID, w.source.Branch)
	if err != nil {
		t.Fatal(err)
	}
	return authority.Generation
}

// A merge of main with conflicts is not made: the step works on the branch
// as it is, and the run records which files conflict, before each step that
// meets them. Nobody is sent it (no chat can be open while the run holds the
// change): the record is what the change page and forge wait -c read, with
// the catch-up command, until the branch contains that main.
func TestMainThatConflictsIsRecordedForTheChangeUntilItCatchesUp(t *testing.T) {
	w := newMainMergeWorld(t)
	gen := w.generation(t)
	tip := w.commit(t, w.source.Branch, map[string]string{"README.md": "the change's readme\n"})
	main := w.commit(t, "main", map[string]string{"README.md": "main's readme\n"})
	refs := w.git("for-each-ref")
	for range 2 {
		if got := w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, gen, w.source, tip); got.Commit != "" {
			t.Fatalf("a conflicting merge was made: %+v", got)
		}
	}
	if after := w.git("for-each-ref"); after != refs {
		t.Fatalf("a conflicting merge moved a ref:\n%s\nthen\n%s", refs, after)
	}
	notices := w.conflictNotices(t)
	if len(notices) != 2 || notices[0]["main"] != main || notices[0]["by"] != "forge" || notices[0]["step"] != w.step.Name || !slices.Equal(notices[0]["conflicts"].([]any), []any{"README.md"}) {
		t.Fatalf("the run's record of the conflict before each step: %+v", notices)
	}

	conflict := w.mainConflict(t)
	if conflict == nil || conflict.Main != main || conflict.Step != w.step.Name || !slices.Equal(conflict.Files, []string{"README.md"}) ||
		conflict.CatchUp != repositoryedit.CatchUpCommand("main", w.source.Branch) {
		t.Fatalf("the change's unresolved conflict: %+v", conflict)
	}

	// The change catches up (in its chat, resolving README.md): the
	// conflict is no longer the change's.
	w.catchUp(t, "README.md", "both readmes\n")
	if conflict := w.mainConflict(t); conflict != nil {
		t.Fatalf("a change that caught up still shows the conflict: %+v", conflict)
	}
}

// mainConflict is the change's conflict as its page reads it: the newest
// recorded across its runs, while the branch does not contain that main.
func (w *mainMergeWorld) mainConflict(t *testing.T) *repositoryedit.MainConflict {
	t.Helper()
	latest, err := w.work.LatestChangeEvent(w.ctx, w.run.ChangeID, repositoryedit.MainConflictNotice)
	if err != nil {
		t.Fatal(err)
	}
	return repositoryedit.MainConflictIn(w.ctx, w.f.hub, w.source.Branch, latest)
}

// catchUp merges main into the change's branch as its chat would,
// resolving every conflict with resolution.
func (w *mainMergeWorld) catchUp(t *testing.T, file, resolution string) string {
	t.Helper()
	w.git("checkout", "-q", w.source.Branch)
	_ = gittest.Command("-C", w.f.hub, "merge", "-q", "main").Run()
	if err := os.WriteFile(filepath.Join(w.f.hub, file), []byte(resolution), 0o644); err != nil {
		t.Fatal(err)
	}
	w.git("add", file)
	w.git("commit", "-q", "--no-edit")
	tip := w.git("rev-parse", "HEAD")
	w.git("checkout", "-q", "main")
	return tip
}

// The change shows its newest conflict: once it has caught up with the
// main of an earlier conflict, a later main that conflicts again is the one
// shown, with that main and step, not hidden behind the earlier one.
func TestTheChangeShowsItsNewestConflictWithMain(t *testing.T) {
	w := newMainMergeWorld(t)
	gen := w.generation(t)
	tip := w.commit(t, w.source.Branch, map[string]string{"README.md": "the change's readme\n"})
	first := w.commit(t, "main", map[string]string{"README.md": "main's readme\n"})
	w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, gen, w.source, tip)
	if c := w.mainConflict(t); c == nil || c.Main != first {
		t.Fatalf("the first conflict: %+v", c)
	}
	tip = w.catchUp(t, "README.md", "both readmes\n")
	if c := w.mainConflict(t); c != nil {
		t.Fatalf("a change that caught up shows %+v", c)
	}
	second := w.commit(t, "main", map[string]string{"README.md": "main's readme, again\n"})
	w.f.exec.mergeMainFirst(w.ctx, w.run, w.step, gen, w.source, tip)
	if c := w.mainConflict(t); c == nil || c.Main != second || !slices.Equal(c.Files, []string{"README.md"}) {
		t.Fatalf("the newer conflict is %+v, want main %s (the earlier %s is merged)", c, second, first)
	}
}
