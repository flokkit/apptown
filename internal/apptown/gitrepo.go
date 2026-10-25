package apptown

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Git-backed state, phase G1 (design/architecture/git-state.md): local-authoritative repos.
// A project with default_branch != '' is repo-backed: every run gets branch
// apptown/run-<id> checked out as a workspace; the engine commits per completed
// step with AppTown-Run-Id trailers; run completion merges --no-ff into the
// default branch. No remote required for any of it.

// --- workspaces are self-contained clones ------------------------------------
//
// A change/run workspace is a full local clone of the project repo (the
// HUB), not a `git worktree`. Worktrees carried a .git POINTER FILE to a
// host-absolute gitdir — dead inside every container, so agents lived with
// "git doesn't work here" and fell back to memory-based file lists
// (complaint #9). A clone is self-contained: containers get real
// status/diff/log/commit. Current clones use --no-hardlinks, so they
// do not borrow their hub's object store.
//
// The hub holds each change's branch; it moves only through scm.Branches,
// which checks who holds the branch and moves it forward from the value it
// read (a workflow's restart may rewind). The hub is the authority for main;
// workspaces never see it move until recreated. Containers see no remote at
// all — transport is the host's job.

// RecoverLandings inspects admitted old Git effects: branch publications
// first, then gives running runs admitted before guarded publication their
// branch, then inspects merges. It never republishes, replays a merge or resumes a
// workflow; a publication or history it cannot settle holds its branch until
// an operator resolves it.
func (a *App) RecoverLandings(ctx context.Context) error {
	published, err := a.branches.Recover(ctx)
	if err != nil {
		return fmt.Errorf("branch publication inspection: %w", err)
	}
	if published.Inspected > 0 {
		log.Printf("startup repair: branch publications inspected=%d settled=%d unknown=%d", published.Inspected, published.Settled, published.Unknown)
	}
	summary, err := a.Work.InspectPendingMerges(workflow.SCMInspectionContext(ctx, a.DB), mergeEffectsFor(a).Driver())
	// A merge that could not be inspected stays pending and holds its
	// landing; it does not stop AppTown from starting.
	if errors.Is(err, scm.ErrLeftPending) {
		log.Printf("startup repair: %v; each holds its landing until inspected again", err)
	} else if err != nil {
		return err
	}
	if summary.Inspected+summary.Failed > 0 {
		log.Printf("startup repair: merge inspection inspected=%d recorded=%d cancelled=%d unresolved=%d left pending=%d", summary.Inspected, summary.Recorded, summary.Cancelled, summary.Unresolved, summary.Failed)
	}
	return nil
}
