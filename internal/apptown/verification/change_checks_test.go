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
	f := newScannerOwnerFixture(t)
	ways := wow.New(f.DB, wow.Dependencies{})
	library := wow.NewLibrary(f.DB)
	if err := library.StockInsert(t.Context(), wow.StockBlock{Name: "checks", Body: "Check the source.", ChecksJSON: `[{"name":"style","appliesTo":["repository"],"evaluator":"sh","sh":"test ! -f failure","severity":"medium","expects":"clean"}]`}); err != nil {
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
	out, err := changes.Check(f.Context, ch, Selection{Commit: commits[1]})
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
