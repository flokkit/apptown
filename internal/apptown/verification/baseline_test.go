package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
)

func TestBaselineClassificationCarriesItsProvenance(t *testing.T) {
	t.Parallel()
	// The split rides on the findings themselves.
	checks := []conformance.DeclaredCheck{
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "occ", Evaluator: "sh", Occurrences: "lines"}},
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "bool", Evaluator: "sh"}},
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "new", Evaluator: "sh"}},
	}
	b := &baselineSet{failing: map[string]bool{"guidance|b@1|occ|": true, "guidance|b@1|bool|": true},
		items: map[string][]string{"guidance|b@1|occ|": {"x"}}, unknown: map[string]bool{}}
	findings := []conformance.Finding{
		{Finding: check.Finding{Fingerprint: "guidance|b@1|occ|", Items: []string{"x"}}},
		{Finding: check.Finding{Fingerprint: "guidance|b@1|bool|"}},
		{Finding: check.Finding{Fingerprint: "guidance|b@1|new|"}},
	}
	results := Results{{Block: "b", Version: 1, Check: "occ"}, {Block: "b", Version: 1, Check: "bool"}, {Block: "b", Version: 1, Check: "new"}}
	own, pre, und := classifyAgainstBase(results, findings, checks, b, workflow.AcceptanceScope{})
	if len(own) != 1 || len(pre) != 1 || len(und) != 1 {
		t.Fatalf("split: own %d pre %d und %d", len(own), len(pre), len(und))
	}
	if findings[0].Regression != "preexisting" || findings[1].Regression != "undecided" || findings[2].Regression != "introduced" {
		t.Fatalf("each finding says how it read: %+v", findings)
	}
}

// Every gate compares the commit it checks with that commit's fork from
// main (workflowProvenanceBase, Changes.forkBase): for a working run and a
// landing alike, and for work that caught up with main in a commit the
// change's published branch does not hold yet (a step's scratch commit).
// main's own work is then not the change's. baseProvenance says whether a
// base is on the protected branch.
//
// Kills: the guidance baseline preferring the recorded base, and a base read
// from the published branch instead of the checked commit (fallback audit
// F18).
func TestEveryGateComparesTheCheckedCommitWithItsFork(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "baseline", Name: "Baseline"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.New(db, nil).SetDefaultBranch(context.Background(), pid, "main"); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	repo := filepath.Join(dataDir, "workspaces", "project-"+pid.String())
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	recorded := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "change")
	git("commit", "-q", "--allow-empty", "-m", "the change's work")
	git("checkout", "-q", "main")
	git("commit", "-q", "--allow-empty", "-m", "main moved on")
	mainNow := git("rev-parse", "HEAD")
	// The step catches up with main in a commit only its scratch work holds.
	git("checkout", "-q", "--detach", "change")
	git("merge", "-q", "--no-edit", "-m", "catch up", "main")
	caughtUp := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")

	source := workflow.Source{ProjectID: pid, ChangeID: idstest.N(1), Slug: "baseline", DefaultBranch: "main", Branch: "change", RootRole: "working", ChangeBaseSHA: recorded}
	for _, role := range []string{"working", "landing"} {
		source.RootRole = role
		if got, err := workflowProvenanceBase(t.Context(), dataDir, source, caughtUp); err != nil || got != mainNow {
			t.Fatalf("%s run: base %s %v, want main as the checked commit merged it (%s)", role, got, err, mainNow)
		}
	}
	changes := NewChanges(ChangeDependencies{DB: db, DataDir: dataDir})
	ch := &workflow.Change{ID: idstest.N(1), ProjectID: pid, Branch: "change", BaseSHA: recorded}
	if got, err := changes.forkBase(t.Context(), ch, caughtUp); err != nil || got != mainNow {
		t.Fatalf("apptown check: base %s %v, want %s", got, err, mainNow)
	}
	// Off a change there is nothing to compare with.
	if got, err := workflowProvenanceBase(t.Context(), dataDir, workflow.Source{ProjectID: pid, Slug: "baseline", DefaultBranch: "main"}, caughtUp); err != nil || got != "" {
		t.Fatalf("no change: %q %v", got, err)
	}

	const unknown = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if on, is := changes.baseProvenance(t.Context(), pid, mainNow); !on || !is {
		t.Fatalf("head: on=%v is=%v", on, is)
	}
	if on, is := changes.baseProvenance(t.Context(), pid, unknown); on || is {
		t.Fatalf("unknown: on=%v is=%v", on, is)
	}
	if on, is := changes.baseProvenance(t.Context(), pid, recorded); !on || is {
		t.Fatalf("ancestor: on=%v is=%v", on, is)
	}
}

// A diff rubric judges the change's own work from its fork, in a working run
// and a landing alike. A landing's own run starts at the change's tip, so
// its recorded base left nothing to judge and the rubric could never be met
// there (demo-smoke#33); after catching up, main's work is not the change's.
//
// Kills: the diff read from the root run's recorded base.
func TestDiffRubricsJudgeTheChangeFromItsFork(t *testing.T) {
	t.Parallel()
	pid := idstest.N(7)
	dataDir := t.TempDir()
	repo := filepath.Join(dataDir, "workspaces", "project-"+pid.String())
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", name)
	}
	git("init", "-q", "-b", "main")
	write("README.md", "base\n")
	git("commit", "-q", "-m", "base")
	forked := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "change")
	write("game.js", "the change's work\n")
	git("commit", "-q", "-m", "the change's work")
	tip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	write("main.txt", "main moved on\n")
	git("commit", "-q", "-m", "main moved on")
	git("checkout", "-q", "--detach", "change")
	git("merge", "-q", "--no-edit", "-m", "catch up", "main")
	caughtUp := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")

	for _, tc := range []struct {
		role, rootBase, commit string
	}{
		{"working", forked, tip},
		// The landing's run was prepared at the change's tip, and checks the
		// candidate that caught up with main.
		{"landing", tip, caughtUp},
	} {
		source := workflow.Source{ProjectID: pid, ChangeID: idstest.N(1), Slug: "diff", DefaultBranch: "main", Branch: "change", RootRole: tc.role, RootBaseSHA: tc.rootBase, ChangeBaseSHA: forked}
		diff, err := workflowDiffText(t.Context(), dataDir, source, checked{Repo: repo, Commit: tc.commit})
		if err != nil {
			t.Fatalf("%s: %v", tc.role, err)
		}
		if !strings.Contains(diff, "the change's work") || strings.Contains(diff, "main moved on") {
			t.Fatalf("%s run: the diff judged is not the change's own work:\n%s", tc.role, diff)
		}
	}
}
