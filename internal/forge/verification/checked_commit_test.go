package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// A check names the commit it answers for. A commit the caller names is
// checked only when the forge has it as the change's work: in the change's
// folder, on the change's published branch, or on one of the change's
// scratch refs (said so). A commit the forge does not have, or has only as a
// sibling's work or another change's scratch work, is refused naming it, and never
// answered with another commit; a string that is not a full object id is
// refused before git reads it. Asking for the published tip reads the
// change's branch in the hub, never the folder, and a branch the hub does
// not have is refused as nothing published. Naming no commit and not asking
// for the published tip reads the change's latest commit on the forge, as
// before.
func TestCheckedCommitIsTheNamedCommitOrARefusal(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "named", Name: "Named"})
	})
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	hub := filepath.Join(dataDir, "workspaces", "project-named")
	folder := filepath.Join(dataDir, "workspaces", "change-00000000-0007-7000-8000-000000000000")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := gittest.Command(append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(hub, 0o755); err != nil {
		t.Fatal(err)
	}
	git(hub, "init", "-q", "-b", "main")
	git(hub, "commit", "-q", "--allow-empty", "-m", "base")
	git(hub, "checkout", "-q", "-b", "other")
	git(hub, "commit", "-q", "--allow-empty", "-m", "a sibling's work")
	sibling := git(hub, "rev-parse", "HEAD")
	git(hub, "checkout", "-q", "-b", "change-00000000-0007-7000-8000-000000000000", "main")
	git(dataDir, "clone", "-q", "-b", "change-00000000-0007-7000-8000-000000000000", hub, folder)
	git(folder, "commit", "-q", "--allow-empty", "-m", "committed in the folder")
	inFolder := git(folder, "rev-parse", "HEAD")
	// Published to the change's branch from elsewhere (a worker): the
	// folder does not have it.
	git(hub, "checkout", "-q", "change-00000000-0007-7000-8000-000000000000")
	git(hub, "commit", "-q", "--allow-empty", "-m", "published work")
	published := git(hub, "rev-parse", "HEAD")
	// A step's work in progress, pushed to its scratch ref (the push door is
	// the forge's Git endpoint: TestAStepPushesOnlyItsOwnScratchRef); and
	// another change's scratch work.
	git(hub, "checkout", "-q", "--detach", published)
	git(hub, "commit", "-q", "--allow-empty", "-m", "the step's work in progress")
	scratch := git(hub, "rev-parse", "HEAD")
	git(hub, "update-ref", scm.ScratchRef(idstest.N(7), idstest.N(3), 1), scratch)
	git(hub, "commit", "-q", "--allow-empty", "-m", "another change's work in progress")
	othersScratch := git(hub, "rev-parse", "HEAD")
	git(hub, "update-ref", scm.ScratchRef(idstest.N(8), idstest.N(4), 1), othersScratch)
	git(hub, "checkout", "-q", "main")
	const elsewhere = "0123456789abcdef0123456789abcdef01234567" // on no repository here

	ch := &workflow.Change{ID: idstest.N(7), ProjectID: pid, Branch: "change-00000000-0007-7000-8000-000000000000"}
	changes := NewChanges(ChangeDependencies{DB: db, DataDir: dataDir})
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		sel       Selection
		repo, sha string
	}{
		{"published on the change's branch", Selection{Commit: published}, hub, published},
		{"committed in the change's folder", Selection{Commit: inFolder}, folder, inFolder},
		{"the published tip, not the folder's HEAD", Selection{Published: true}, hub, published},
		{"none named: the change's latest", Selection{}, folder, inFolder},
		{"a step's work in progress on the change's scratch ref", Selection{Commit: scratch}, hub, scratch},
	} {
		repo, sha, inScratch, err := changes.checkedCommit(ctx, ch, tc.sel)
		if err != nil || repo != tc.repo || sha != tc.sha || inScratch != (sha == scratch) {
			t.Fatalf("%s: read %s at %s (scratch %v, %v), want %s at %s", tc.name, sha, repo, inScratch, err, tc.sha, tc.repo)
		}
	}
	for name, commit := range map[string]string{"not on the forge": elsewhere, "only on a sibling's branch": sibling, "only on another change's scratch ref": othersScratch} {
		_, sha, err := changes.CheckedCommit(ctx, ch, Selection{Commit: commit})
		var refused *CommitNotOnForgeError
		if !errors.As(err, &refused) || refused.Commit != commit || sha != "" || fault.KindOf(err) != fault.NotFound {
			t.Fatalf("%s: read %q (%v), want a refusal naming %s", name, sha, err, commit)
		}
		for _, words := range []string{"commit " + commit + " is not on the forge yet", "it becomes available once the step's work is collected", "forge check --published"} {
			if !strings.Contains(err.Error(), words) {
				t.Fatalf("%s: the refusal does not say %q: %v", name, words, err)
			}
		}
	}
	// Each of these would name a commit if git read it (the folder's HEAD, an
	// abbreviation of its commit, an option); none is a full object id.
	for _, commit := range []string{"HEAD", inFolder[:12], strings.ToUpper(inFolder), "--output=" + inFolder[:31], "refs/heads/change-00000000-0007-7000-8000-000000000000"} {
		_, sha, err := changes.CheckedCommit(ctx, ch, Selection{Commit: commit})
		if fault.KindOf(err) != fault.Invalid || sha != "" || !strings.Contains(err.Error(), "not a full commit id") {
			t.Fatalf("%q: read %q (%v), want it refused as not a commit id", commit, sha, err)
		}
	}
	// A commit and the published tip together is not one question.
	if _, sha, err := changes.CheckedCommit(ctx, ch, Selection{Commit: inFolder, Published: true}); fault.KindOf(err) != fault.Invalid || sha != "" {
		t.Fatalf("a commit and the published tip: read %q (%v), want it refused as invalid", sha, err)
	}
	// A change whose branch the hub does not have has nothing published,
	// though its folder has commits.
	unpublished := &workflow.Change{ID: idstest.N(7), ProjectID: pid, Branch: "change-00000000-0008-7000-8000-000000000000"}
	for _, c := range []*workflow.Change{unpublished, {ID: idstest.N(7), ProjectID: pid}} {
		_, sha, err := changes.CheckedCommit(ctx, c, Selection{Published: true})
		var nothing *NothingPublishedError
		if !errors.As(err, &nothing) || sha != "" || fault.KindOf(err) != fault.NotFound || !strings.Contains(err.Error(), "nothing published to check") && !strings.Contains(err.Error(), "nothing is published to check") {
			t.Fatalf("branch %q: read %q (%v), want a refusal: nothing published", c.Branch, sha, err)
		}
	}
	// The whole check refuses before anything runs: this Changes has no
	// checks, scanners or evaluator to run.
	if out, err := changes.Check(ctx, unpublished, Selection{Published: true}); !errors.As(err, new(*NothingPublishedError)) || out != nil {
		t.Fatalf("a check of an unpublished change answered %v (%v)", out, err)
	}
	if out, err := changes.Check(ctx, ch, Selection{Commit: elsewhere}); !errors.As(err, new(*CommitNotOnForgeError)) || out != nil {
		t.Fatalf("a check of a commit not on the forge answered %v (%v)", out, err)
	}
}
