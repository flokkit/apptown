package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

var errWorkspaceChanged = errors.New("workspace source changed; inspect and prepare again")

type ChangeRef struct{ ID, ProjectID ids.ID }

type changeWorkspaceSource struct {
	Ref                                             ChangeRef
	Slug, DefaultBranch, Branch, BaseSHA, LandedSHA string
	State                                           ChangeState
	RunID                                           ids.ID
}

type runWorkspaceGuard struct {
	State    RunState
	Stage    string
	Revision int64
}

type workspaces struct {
	db    *database.DB
	git   Git
	locks *scm.RepositoryLocks
	clocked
}

func newWorkspaces(db *database.DB, git Git, locks *scm.RepositoryLocks) workspaces {
	return workspaces{db: db, git: git, locks: locks}
}

func (w workspaces) PrepareChange(ctx context.Context, ref ChangeRef) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	release, err := w.locks.Acquire(ctx, ref.ProjectID)
	if err != nil {
		return err
	}
	defer release()
	before, err := readChangeWorkspace(ctx, w.db, ref)
	if err != nil {
		return err
	}
	if before.DefaultBranch == "" || before.Branch == "" {
		return nil
	}
	repository, err := w.git.Paths().Repository(project.RepositoryRef{ID: ref.ProjectID, Slug: before.Slug})
	if err != nil {
		return err
	}
	result, err := w.git.EnsureClone(ctx, CloneRequest{Repository: repository, Workspace: w.git.Paths().ChangeWorkspace(ref.ID), Branch: before.Branch, DefaultBranch: before.DefaultBranch, RequireBranch: before.BaseSHA != ""})
	if err != nil {
		return err
	}
	// The change's new holder (a chat) finds the folder brought to the
	// branch as published, with its previous worker's leftovers committed.
	// A diverged folder is left for the chat to merge: the caller says so.
	var diverged error
	if !result.Created {
		if err := w.git.BringToBranch(ctx, result.Path, repository, before.Branch); errors.Is(err, ErrFolderDiverged) {
			diverged = err
		} else if err != nil {
			return err
		}
	}
	// Its new holder's own history is read from here on (scm.Behind).
	if diverged == nil {
		if err := scm.ClearSaved(result.Path); err != nil {
			return err
		}
	}
	if err := w.db.InTx(ctx, func(tx *database.Tx) error {
		if err := lockWorkspaceProject(ctx, tx, ref.ProjectID); err != nil {
			return err
		}
		return recordChangeWorkspaceIn(ctx, tx, before, result.Tip)
	}); err != nil {
		return err
	}
	if err := w.git.SyncAttachments(ctx, ref.ID, w.git.Paths().ChangeWorkspace(ref.ID)); err != nil {
		return err
	}
	return diverged
}

func readChangeWorkspace(ctx context.Context, db database.Queryer, ref ChangeRef) (changeWorkspaceSource, error) {
	var s changeWorkspaceSource
	s.Ref = ref
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return s, fmt.Errorf("invalid Change workspace scope")
	}
	row, err := workflowsql.New(db.SQL()).ChangeWorkspaceSourceMeta(ctx, workflowsql.ChangeWorkspaceSourceMetaParams{ID: ref.ID, ProjectID: ref.ProjectID})
	if err != nil {
		return s, database.Missing(err)
	}
	state, err := ParseChangeState(row.State)
	if err != nil {
		return s, err
	}
	s.Slug, s.DefaultBranch, s.Branch, s.BaseSHA, s.LandedSHA, s.State, s.RunID = row.Slug, row.DefaultBranch, row.Branch, row.BaseSha, row.LandedSha, state, row.RunID
	return s, nil
}

func lockWorkspaceProject(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, projectID)
	return err
}

// The same participant publishes bases for eager Change preparation and Run
// preparation. It never overwrites an imported or previously witnessed base.
func recordChangeWorkspaceIn(ctx context.Context, tx *database.Tx, before changeWorkspaceSource, tip string) error {
	q := workflowsql.New(tx.SQL())
	if _, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: before.Ref.ID, ProjectID: before.Ref.ProjectID}); err != nil {
		return err
	}
	current, err := readChangeWorkspace(ctx, tx, before.Ref)
	if err != nil {
		return err
	}
	if current != before || tip == "" {
		return errWorkspaceChanged
	}
	if before.BaseSHA != "" {
		return nil
	}
	n, err := q.SetChangeWorkspaceBase(ctx, workflowsql.SetChangeWorkspaceBaseParams{Tip: tip, Branch: before.Branch, ChangeID: before.Ref.ID, ProjectID: before.Ref.ProjectID,
		OldBranch: before.Branch, OldBaseSha: before.BaseSHA, OldLandedSha: before.LandedSHA, OldState: before.State.String(), OldRunID: before.RunID})
	if err != nil {
		return err
	}
	if n != 1 {
		return errWorkspaceChanged
	}
	return nil
}

func (w workspaces) PrepareRun(ctx context.Context, ref RunRef) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	release, err := w.locks.Acquire(ctx, ref.ProjectID)
	if err != nil {
		return err
	}
	defer release()
	// One snapshot freezes source ownership and run admission before the effect.
	tx, err := w.db.ReadSnapshot(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	before, err := ReadSourceIn(ctx, tx, ref)
	if err != nil {
		return err
	}
	guard, err := readRunWorkspaceGuard(ctx, tx, ref)
	if err != nil {
		return err
	}
	if err := guard.State.RequireRunning(); err != nil {
		return err
	}
	if before.RootRunID != ref.ID {
		return fmt.Errorf("only a root run prepares a workspace")
	}
	var change *changeWorkspaceSource
	if !before.ChangeID.IsZero() {
		value, err := readChangeWorkspace(ctx, tx, ChangeRef{ID: before.ChangeID, ProjectID: ref.ProjectID})
		if err != nil {
			return err
		}
		change = &value
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !before.RepoBacked() {
		return nil
	}
	// A writing run works in its change's folder; a landing in a copy of
	// its own. A run already under way requires its branch.
	repository, err := before.Repository(w.git.Paths().DataDir)
	if err != nil {
		return err
	}
	workspace, err := before.Workspace(w.git.Paths().DataDir)
	if err != nil {
		return err
	}
	result, err := w.git.EnsureClone(ctx, CloneRequest{Repository: repository, Workspace: workspace, Branch: before.Branch, DefaultBranch: before.DefaultBranch,
		RequireBranch: before.ChangeBaseSHA != "" || before.RunBaseSHA != ""})
	if err != nil {
		return err
	}
	// A folder made now holds its branch: a record left for an earlier
	// folder of the same name (removed since) is not its.
	if result.Created {
		if err := scm.ClearSaved(result.Path); err != nil {
			return err
		}
	}
	// A writing run taking its change's folder first brings it to the branch
	// as published, committing the previous worker's leftovers, and only then
	// fixes its restart point: a restart never rewinds past work the run did
	// not make. A diverged folder refuses the run.
	if !result.Created && !before.ChangeID.IsZero() && before.RootRole != "landing" && before.RunBaseSHA == "" {
		if err := w.git.BringToBranch(ctx, result.Path, repository, before.Branch); err != nil {
			return err
		}
		tip, err := w.git.BranchTip(ctx, result.Path, before.Branch)
		if err != nil {
			return err
		}
		result.Tip = tip
		// The run's own history is read from here on (scm.Behind).
		if err := scm.ClearSaved(result.Path); err != nil {
			return err
		}
	}
	if err := w.db.InTx(ctx, func(tx *database.Tx) error {
		if err := lockWorkspaceProject(ctx, tx, ref.ProjectID); err != nil {
			return err
		}
		q := workflowsql.New(tx.SQL())
		if _, err := q.ScopedRunForUpdate(ctx, workflowsql.ScopedRunForUpdateParams{ID: ref.ID, ProjectID: ref.ProjectID}); err != nil {
			return err
		}
		current, err := ReadSourceIn(ctx, tx, ref)
		if err != nil {
			return err
		}
		next, err := readRunWorkspaceGuard(ctx, tx, ref)
		if err != nil {
			return err
		}
		if current != before || next != guard {
			return errWorkspaceChanged
		}
		// A detached Run may already have a historical base. Preparation fills absence
		// and does not rewrite that history merely because its directory was recreated.
		if before.RunBaseSHA == "" {
			n, err := q.SetRunWorkspaceBase(ctx, workflowsql.SetRunWorkspaceBaseParams{Tip: result.Tip, RunID: ref.ID, ProjectID: ref.ProjectID,
				OldBaseSha: before.RunBaseSHA, OldStatus: guard.State.String(), OldStage: guard.Stage, OldRevision: guard.Revision})
			if err != nil {
				return err
			}
			if n != 1 {
				return errWorkspaceChanged
			}
		}
		if change != nil {
			if err := recordRunChangeWorkspaceIn(ctx, tx, *change, before.Branch, result.Tip); err != nil {
				return err
			}
		}
		// Every successful admission has a receipt, including explicit preparation
		// after a previous post-clone transaction failure. No failed history is guessed.
		if err := recordWorkspaceEventIn(ctx, tx, ref.ID, workspacePreparedReceipt{Branch: before.Branch, Tip: result.Tip, Created: result.Created}, w.now()); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}
	if !before.ChangeID.IsZero() {
		return w.git.SyncAttachments(ctx, before.ChangeID, workspace)
	}
	return nil
}

func readRunWorkspaceGuard(ctx context.Context, db database.Queryer, ref RunRef) (runWorkspaceGuard, error) {
	row, err := workflowsql.New(db.SQL()).RunWorkspaceGuardMeta(ctx, workflowsql.RunWorkspaceGuardMetaParams{ID: ref.ID, ProjectID: ref.ProjectID})
	if err != nil {
		return runWorkspaceGuard{}, database.Missing(err)
	}
	state, err := ParseRunState(row.Status)
	return runWorkspaceGuard{State: state, Stage: row.CurrentStage, Revision: row.RetryRevision}, err
}

func recordRunChangeWorkspaceIn(ctx context.Context, tx *database.Tx, before changeWorkspaceSource, branch, tip string) error {
	// Run admission may choose the canonical branch before an uplift has named one.
	// Check the old snapshot before naming it, and join the same guarded base write.
	if before.Branch != "" && before.Branch != branch {
		return errWorkspaceChanged
	}
	if before.Branch == "" {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: before.Ref.ID, ProjectID: before.Ref.ProjectID}); err != nil {
			return err
		}
		current, err := readChangeWorkspace(ctx, tx, before.Ref)
		if err != nil {
			return err
		}
		if current != before {
			return errWorkspaceChanged
		}
		n, err := q.NameChangeWorkspaceBranch(ctx, workflowsql.NameChangeWorkspaceBranchParams{Branch: branch, ChangeID: before.Ref.ID, ProjectID: before.Ref.ProjectID})
		if err != nil {
			return err
		}
		if n != 1 {
			return errWorkspaceChanged
		}
		before.Branch = branch
	}
	return recordChangeWorkspaceIn(ctx, tx, before, tip)
}

// PrepareWorkspace readies a change's checkout for whoever may work on it.
func (s *Service) PrepareWorkspace(ctx context.Context, changeID ids.ID) error {
	project, _, err := s.changeScope(ctx, changeID, actionBeginChange)
	if err != nil {
		return err
	}
	return s.workspaces.PrepareChange(ctx, ChangeRef{ID: changeID, ProjectID: project})
}
