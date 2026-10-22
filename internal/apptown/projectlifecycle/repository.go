package projectlifecycle

import (
	"context"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
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
