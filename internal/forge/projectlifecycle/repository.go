package projectlifecycle

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/ids"
)

// InitializeRepository adopts workspace content or creates an empty Git hub,
// installs the push fence, then records its default branch and its main. A
// nonempty receiptActor requests the existing informational HTTP receipt;
// internal setup and MCP historically omit that event. Required command
// errors still propagate. It takes the project's repository lock; a caller
// that already holds it (a seed) uses initializeHeld.
func (s *Service) InitializeRepository(ctx context.Context, p *project.Project, defaultBranch, receiptActor string) error {
	release, err := s.repositories.Acquire(ctx, p.ID)
	if err != nil {
		return err
	}
	defer release()
	// Once the lock is held, the history is made and recorded together,
	// though the caller stops.
	ctx = context.WithoutCancel(ctx)
	if err := s.initializeHeld(ctx, p, defaultBranch, scm.ViaInit); err != nil {
		return err
	}
	if receiptActor != "" {
		audit.At(s.now()).BestEffort(ctx, s.db, receiptActor, p.ID, "project.repo.initialized", p.Slug, map[string]any{"defaultBranch": p.DefaultBranch})
	}
	return nil
}

// initializeHeld is InitializeRepository for a caller holding the project's
// repository lock. The default branch and the main read back from the hub
// are recorded in one transaction, under the door that made the history.
func (s *Service) initializeHeld(ctx context.Context, p *project.Project, defaultBranch string, via scm.MainVia) error {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	dir := hubDir(s.dataDir, p.Slug)
	if err := scm.InitializeRepository(ctx, dir, defaultBranch); err != nil {
		return err
	}
	move, err := scm.PrepareMainMove(ctx, s.db, dir, p.ID, defaultBranch)
	if err != nil {
		return err
	}
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := project.SetDefaultBranchIn(ctx, tx, p.ID, defaultBranch); err != nil {
			return err
		}
		return move.RecordIn(ctx, tx, via, ids.Nil, s.now())
	}); err != nil {
		return err
	}
	p.DefaultBranch = defaultBranch
	return nil
}

func hubDir(dataDir, slug string) string {
	return filepath.Join(dataDir, "workspaces", "project-"+slug)
}

// BackfillReport counts what BackfillRecordedMain did.
type BackfillReport struct {
	Recorded, Kept, Unknown int
	Unreadable              []string // slugs whose main could not be read
}

// BackfillRecordedMain gives every repository-backed project without a
// recorded main the hub's current main, as backfill: "this is where main was
// when recording began", not a witnessed landing. A project already recorded
// is kept; one whose main cannot be read stays unknown. It takes no
// repository lock, which is per process: run it only while no forged serves
// this database (internal/forge/scm/README.md, the recorded main).
// now is the forge's clock; nil is the real one.
func BackfillRecordedMain(ctx context.Context, db *database.DB, dataDir string, now func() time.Time) (BackfillReport, error) {
	at := clock.Or(now)()
	var report BackfillReport
	projects, err := project.New(db, nil).All(ctx)
	if err != nil {
		return report, err
	}
	for _, p := range projects {
		if p.DefaultBranch == "" {
			continue
		}
		if _, known, err := scm.ReadMain(ctx, db, p.ID); err != nil {
			return report, err
		} else if known {
			report.Kept++
			continue
		}
		move, err := scm.PrepareMainMove(ctx, db, hubDir(dataDir, p.Slug), p.ID, p.DefaultBranch)
		if err != nil {
			return report, err
		}
		if move.Read() == "" {
			report.Unknown++
			report.Unreadable = append(report.Unreadable, p.Slug)
			continue
		}
		if err := db.InTx(ctx, func(tx *database.Tx) error { return move.RecordIn(ctx, tx, scm.ViaBackfill, ids.Nil, at) }); err != nil {
			return report, fmt.Errorf("recording %s's main: %w", p.Slug, err)
		}
		report.Recorded++
	}
	return report, nil
}
