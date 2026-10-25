package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A review holds a change to its own work: an active change that caught up
// with main and added nothing has nothing in scope, not main's files, so an
// honest report of no code passes the scope rule. The base is the fork, as
// every gate reads it, never the historical rule for a change's views.
//
// Kills: reviewBase on scm.ChangeBase, whose recorded base for a change at
// main's commit put main's later files in scope (fallback audit F18).
func TestAReviewOfACaughtUpChangeHoldsNothingOfMains(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.invalid")
	git("commit", "-q", "--allow-empty", "-m", "recorded base")
	recorded := git("rev-parse", "HEAD")
	git("branch", "change")
	if err := os.WriteFile(filepath.Join(repo, "main-only.txt"), []byte("main's work"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "main-only.txt")
	git("commit", "-q", "-m", "main advances")
	git("checkout", "-q", "change")
	git("merge", "-q", "--ff-only", "main")
	source := workflow.Source{ProjectID: idstest.N(2), ChangeID: idstest.N(1), Slug: "review", DefaultBranch: "main", Branch: "change", ChangeBaseSHA: recorded}
	_, files, err := hubScope(repo, source, git("rev-parse", "HEAD"))(t.Context())
	if err != nil || len(files) != 0 {
		t.Fatalf("a caught-up change with no work of its own holds %+v (%v)", files, err)
	}
}
