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
// main (WorkflowProvenanceBase, Changes.forkBase): for a working run and a
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
		if got, err := WorkflowProvenanceBase(t.Context(), dataDir, source, caughtUp); err != nil || got != mainNow {
			t.Fatalf("%s run: base %s %v, want main as the checked commit merged it (%s)", role, got, err, mainNow)
		}
	}
	changes := NewChanges(ChangeDependencies{DB: db, DataDir: dataDir})
	ch := &workflow.Change{ID: idstest.N(1), ProjectID: pid, Branch: "change", BaseSHA: recorded}
	if got, err := changes.forkBase(t.Context(), ch, caughtUp); err != nil || got != mainNow {
		t.Fatalf("apptown check: base %s %v, want %s", got, err, mainNow)
	}
	// Off a change there is nothing to compare with.
	if got, err := WorkflowProvenanceBase(t.Context(), dataDir, workflow.Source{ProjectID: pid, Slug: "baseline", DefaultBranch: "main"}, caughtUp); err != nil || got != "" {
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
