package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/stock"
)

// A change is checked against the revision its own run reads. When that run
// or its recorded adoption cannot be read, the check fails: the project's
// current adoption is another revision's law, not a stand-in. A change with
// no run reads the project's current receipt, and a project that has
// adopted nothing has no checks.
//
// Kills: the fall-through from an unreadable run to the current adoption
// (fallback audit, verification/declarations.go).
func TestAChangesChecksAreItsRunsRevisionOrAnError(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	create := func(slug string, revision int64) ids.ID {
		t.Helper()
		pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
			if err != nil {
				return id, err
			}
			return id, adoption.InitializeIn(t.Context(), tx, id, revision, "")
		})
		if err != nil {
			t.Fatal(err)
		}
		return pid
	}
	pid := create("snapshot", 0)
	changes := NewChanges(ChangeDependencies{DB: db, DataDir: t.TempDir()})
	// No such run: as a run whose read fails.
	gone := &workflow.Change{ID: idstest.N(1), ProjectID: pid, RunID: idstest.N(99)}
	if checks, ad, err := changes.changeChecks(t.Context(), gone); err == nil {
		t.Fatalf("an unreadable run gave checks %v from %+v", checks, ad)
	}
	runless := &workflow.Change{ID: idstest.N(2), ProjectID: pid}
	if checks, ad, err := changes.changeChecks(t.Context(), runless); err != nil || checks != nil || ad != nil {
		t.Fatalf("a project with no adoption: %v, %+v, %v; want no checks", checks, ad, err)
	}
	// A current revision whose receipt is missing is unknown, not "nothing
	// adopted" (InitializeIn records the revision without its receipt).
	lost := &workflow.Change{ID: idstest.N(3), ProjectID: create("lost-receipt", 3)}
	if checks, _, err := changes.changeChecks(t.Context(), lost); !errors.Is(err, adoption.ErrRevisionUnavailable) {
		t.Fatalf("a revision without its receipt: %v, %v; want the revision unavailable", checks, err)
	}
}

// An introduced finding below failOn stays visible and explains why it does not block.
func TestChangesCheckExplainsIntroducedFindingsBelowFailOn(t *testing.T) {
	t.Parallel()
	out, err := changesCheckWithDeclarations(t, `[{"name":"style","appliesTo":["repository"],"evaluator":"sh","sh":"test ! -f failure","severity":"medium","expects":"clean"}]`)

	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true || out["failing"] != 0 {
		t.Fatalf("below failOn should not block: %+v", out)
	}
	results := out["checks"].(Results)
	if len(results) != 1 || results[0].Verdict != workflow.VerdictFail || results[0].Preexisting || results[0].Undecided || results[0].Note != "severity medium is below high: shown, does not block" {
		t.Fatalf("introduced result: %+v", results)
	}
}

type introducedCheckEffects struct{ Effects }

func (e introducedCheckEffects) Run(_ context.Context, req execution.Request) execution.Outcome {
	exit := 0
	if _, err := os.Stat(filepath.Join(req.Workspace, "failure")); err == nil {
		exit = 1
	}
	return execution.Outcome{Termination: execution.Exited, ExitCode: exit, Output: []byte("style finding\n")}
}

func changesCheckWithDeclarations(t *testing.T, checksJSON string) (map[string]any, error) {
	t.Helper()
	f := newScannerOwnerFixture(t)
	ways := wow.New(f.DB, wow.Dependencies{})
	library := wow.NewLibrary(f.DB)
	if err := library.StockInsert(t.Context(), wow.StockBlock{Name: "checks", Body: "Check the source.", ChecksJSON: checksJSON}); err != nil {
		t.Fatal(err)
	}
	block, err := ways.BlockByName(t.Context(), "checks")
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = []adoption.ReceiptBlock{{Name: block.Name, BlockID: block.ID, Version: 1}}
	if _, err := database.Transact(t.Context(), f.DB, func(tx *database.Tx) (bool, error) {
		return true, adoption.New(nil, adoption.Dependencies{}).RecordIn(t.Context(), tx, f.ProjectID, 1, adoption.AdoptionEdit, "checks", receipt, "tester", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	repo := filepath.Join(dataDir, "workspaces", "project-"+f.ProjectID.String())
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	commits := gittest.Repository(t, repo, "main", gittest.Commit{Message: "base", Files: map[string]string{"README.md": "base"}}, gittest.Commit{Message: "change", Files: map[string]string{"failure": "new finding"}})
	gittest.Run(t, repo, "update-ref", "refs/heads/change", commits[1])
	gittest.Run(t, repo, "update-ref", "refs/heads/main", commits[0])
	changes := NewChanges(ChangeDependencies{DB: f.DB, DataDir: dataDir, Scanners: f.Scanners, Adoption: adoption.New(f.DB, adoption.Dependencies{Ways: ways}), Evaluator: NewEvaluator(introducedCheckEffects{Effects: f.Effects}, nil, stock.Versions())})
	ch := &workflow.Change{ID: idstest.N(7), ProjectID: f.ProjectID, Branch: "change", BaseSHA: commits[0]}
	return changes.Check(f.Context, ch, Selection{Commit: commits[1]})
}

func TestChangesCheckReportsLandingJudgementsAsSkipped(t *testing.T) {
	t.Parallel()
	out, err := changesCheckWithDeclarations(t, `[
 {"name":"runtime","appliesTo":["repository"],"evaluator":"runtime","probe":"health","severity":"high","expects":"healthy"},
 {"name":"model","appliesTo":["repository"],"evaluator":"llm","rubric":"clear","severity":"high","expects":"clear"},
 {"name":"irrelevant","appliesTo":["tool:python"],"evaluator":"llm","rubric":"clear","severity":"high","expects":"clear"}
 ]`)
	if err != nil {
		t.Fatal(err)
	}
	rows := out["checks"].(Results)
	if len(rows) != 3 || out["ok"] != true {
		t.Fatalf("missing landing rows: %v", out)
	}
	if rows[0].Check != "runtime" || rows[0].Verdict != workflow.VerdictSkipped || rows[0].Note != "judged at the landing against this change's running preview; apptown check does not run it" {
		t.Fatal(rows)
	}
	if rows[1].Check != "model" || rows[1].Verdict != workflow.VerdictSkipped || rows[1].Note != "judged by a model at the landing" {
		t.Fatal(rows)
	}
	if rows[2].Verdict != workflow.VerdictNotApplicable || rows[2].Detail == "" {
		t.Fatal(rows)
	}
}

func TestChangesCheckKeepsMalformedApplicabilityAnError(t *testing.T) {
	t.Parallel()
	out, err := changesCheckWithDeclarations(t, `[{"name":"invalid","appliesTo":[],"evaluator":"runtime","probe":"health","severity":"high","expects":"healthy"}]`)
	if err == nil || out != nil {
		t.Fatalf("malformed applicability returned an answer: %v %v", out, err)
	}
}
