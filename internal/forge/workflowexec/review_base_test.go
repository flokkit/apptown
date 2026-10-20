package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/workflow"
)

// After a send-back the run that reworks a change starts from the branch's
// tip, not from the change's base. The reviewer's diff and the scope rules
// must still read the same base, the change's, so that an honest report of
// the whole change satisfies both: every file the scope rule holds the
// report to is in the diff the reviewer checks the report against, and a
// report naming every one of them has nothing unclaimed. Both diff readers
// (AppTown's own folder and the hub) are held to it, after the change has
// caught up with main too.
func TestAWholeChangeReportSatisfiesTheReviewAfterASendBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := workflow.Source{Slug: "sendback", DefaultBranch: "main", Branch: "apptown/change-00000000-0001-7000-8000-000000000000", ChangeID: idstest.N(1)}
	repo := source.Repository(dir)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file, msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, file), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	source.ChangeBaseSHA = commit("README.md", "base")
	git("checkout", "-q", "-b", source.Branch)
	// The first round's work, sent back by a person; the rework run starts here.
	source.RunBaseSHA = commit("upgrade.txt", "first round")
	commit("fix.txt", "second round")
	// Main moves and the change catches up with it: main_test.go is main's,
	// not the change's, so neither the diff nor the scope holds the report
	// to it.
	git("checkout", "-q", "main")
	commit("main_test.go", "on main")
	git("checkout", "-q", source.Branch)
	git("merge", "-q", "--no-edit", "-m", "Merge main into "+source.Branch, "main")
	tip := git("rev-parse", "HEAD")

	_, scope, err := hubScope(repo, source, tip)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report := "CHANGED upgrade.txt: the upgrade asked for\nCHANGED fix.txt: what the person sent it back for"
	if unclaimed := repositoryedit.UnclaimedChanges(scope, report); len(unclaimed) != 0 || len(scope) != 2 {
		t.Fatalf("the honest whole-change report leaves %v unclaimed of %+v", unclaimed, scope)
	}
	hubDiff, err := hubReviewDiff(context.Background(), repo, source, tip)
	if err != nil {
		t.Fatal(err)
	}
	base, err := reviewBase(context.Background(), repo, source, "refs/heads/"+source.Branch)
	if err != nil {
		t.Fatal(err)
	}
	for name, diff := range map[string]string{"folder": reviewDiff(t.Context(), dir, source, base), "hub": hubDiff} {
		for _, c := range scope {
			if !strings.Contains(diff, "+++ b/"+c.Path) {
				t.Errorf("the %s diff the reviewer reads lacks %s, which the scope rule holds the report to:\n%s", name, c.Path, diff)
			}
		}
		if strings.Contains(diff, "main_test.go") {
			t.Errorf("the %s diff the reviewer reads holds main's main_test.go as the change's:\n%s", name, diff)
		}
	}
}
