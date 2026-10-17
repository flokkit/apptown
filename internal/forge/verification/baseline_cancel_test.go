package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conformance"
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
	repo, sha := baselineTree(t, "")
	if err := os.MkdirAll(filepath.Join(dataDir, "workspaces"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo, filepath.Join(dataDir, "workspaces", "project-p")); err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(t.Context())
	effects := &stoppingEffects{scannerEffects: f.Effects, stop: cancel}
	changes := NewChanges(ChangeDependencies{DB: f.DB, DataDir: dataDir, Evaluator: NewEvaluator(effects, nil), Scanners: f.Scanners})
	checks := []conformance.DeclaredCheck{{Block: "b", Version: 1, Check: conformance.CheckDeclaration{Name: "script", Evaluator: "sh", Sh: "exit 0"}}}

	changes.baselineFor(stopped, f.ProjectID, checks, sha, "test")
	effects.stop = nil
	again := changes.baselineFor(t.Context(), f.ProjectID, checks, sha, "test")
	if effects.runs != 2 || again == nil || len(again.unknown) != 0 {
		t.Fatalf("baseline after a stopped one: runs %d, %+v; want it asked again and known", effects.runs, again)
	}
}
