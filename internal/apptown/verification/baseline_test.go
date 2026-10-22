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
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "occ", Evaluator: "sh", Occurrences: "lines"}},
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "bool", Evaluator: "sh"}},
		{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "new", Evaluator: "sh"}},
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

// Verification receives a checked Workflow source. Its choice of comparison
// tree is private policy; the fixture needs a repository and project, not App.
func TestBaselineUsesWorkingBaseAndCurrentLandingHead(t *testing.T) {
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
	repo := filepath.Join(dataDir, "workspaces", "project-baseline")
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
	seed := git("rev-parse", "HEAD")
	git("branch", "change")
	const unknown = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	source := workflow.Source{ProjectID: pid, ChangeID: idstest.N(1), Slug: "baseline", DefaultBranch: "main", Branch: "change", RootRole: "working", ChangeBaseSHA: unknown}
	if got := workflowBaseline(t.Context(), dataDir, source); got != unknown {
		t.Fatalf("working comparison lost its recorded base: %s", got)
	}
	source.RootRole = "landing"
	if got := workflowBaseline(t.Context(), dataDir, source); got != seed {
		t.Fatalf("landing comparison is not current main: %s", got)
	}
	changes := NewChanges(ChangeDependencies{DB: db, DataDir: dataDir})
	if on, is := changes.baseProvenance(t.Context(), pid, seed); !on || !is {
		t.Fatalf("head: on=%v is=%v", on, is)
	}
	if on, is := changes.baseProvenance(t.Context(), pid, unknown); on || is {
		t.Fatalf("unknown: on=%v is=%v", on, is)
	}
	git("commit", "-q", "--allow-empty", "-m", "main advanced")
	if got := changes.forkPointSHA(t.Context(), pid, "change"); got != seed {
		t.Fatalf("fork point moved with main: %s", got)
	}
	if on, is := changes.baseProvenance(t.Context(), pid, seed); !on || is {
		t.Fatalf("ancestor: on=%v is=%v", on, is)
	}
}
