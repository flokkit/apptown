package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// stoppingEffects runs a check as passing, or, while stop is set, stops
// the caller as the check runs and answers as a cut-short run does.
type stoppingEffects struct {
	*scannerEffects
	stop func()
	runs int
}

func (e *stoppingEffects) Run(_ context.Context, _ execution.Request) execution.Outcome {
	e.runs++
	if e.stop != nil {
		e.stop()
		return execution.Outcome{Termination: execution.Canceled}
	}
	return execution.Outcome{Termination: execution.Exited}
}

// A baseline its caller stopped part-way is not kept: kept, its unknowns
// would mark the next check's findings undecided (shown, not blocking)
// until the retry window passed.
//
// Kills: a stopped baseline cached for the next check.
func TestAStoppedBaselineIsNotKept(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	dataDir := t.TempDir()
	repo, sha := baselineTree(t, "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true}\n")
	if err := os.MkdirAll(filepath.Join(dataDir, "workspaces"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo, filepath.Join(dataDir, "workspaces", "project-p")); err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(t.Context())
	effects := &stoppingEffects{scannerEffects: f.Effects, stop: cancel}
	changes := NewChanges(ChangeDependencies{DB: f.DB, DataDir: dataDir, Evaluator: NewEvaluator(effects, nil, stock.Versions()), Scanners: f.Scanners})
	checks := []conformance.DeclaredCheck{{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "script", Evaluator: "sh", Sh: "exit 0"}}}

	changes.baselineFor(stopped, stock.Versions().Current(), f.ProjectID, checks, sha, "test")
	effects.stop = nil
	again := changes.baselineFor(t.Context(), stock.Versions().Current(), f.ProjectID, checks, sha, "test")
	if effects.runs != 2 || again == nil || len(again.unknown) != 0 {
		t.Fatalf("baseline after a stopped one: runs %d, %+v; want it asked again and known", effects.runs, again)
	}
}

// A check not applicable on the base (main declared no part it applies
// to) is known there: nothing for it to find, so a failure on the change's
// head is the change's own, never undecided and so never waved through.
//
// Kills: a base's not-applicable read as unknown.
func TestABaseWhereACheckIsNotApplicableIsKnown(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	dataDir := t.TempDir()
	repo, sha := baselineTree(t, "version: 2\napp:\n  web: {build: ., ports: [\"8080\"], public: true}\n")
	if err := os.MkdirAll(filepath.Join(dataDir, "workspaces"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo, filepath.Join(dataDir, "workspaces", "project-p")); err != nil {
		t.Fatal(err)
	}
	effects := &stoppingEffects{scannerEffects: f.Effects}
	changes := NewChanges(ChangeDependencies{DB: f.DB, DataDir: dataDir, Evaluator: NewEvaluator(effects, nil, stock.Versions()), Scanners: f.Scanners})
	checks := []conformance.DeclaredCheck{{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"tool:go"}, Name: "vet", Evaluator: "sh", Sh: "exit 1"}}}
	base := changes.baselineFor(t.Context(), stock.Versions().Current(), f.ProjectID, checks, sha, "test")
	if effects.runs != 0 || base == nil || len(base.unknown) != 0 || len(base.failing) != 0 {
		t.Fatalf("a base where the check does not apply: runs %d, %+v", effects.runs, base)
	}
}
