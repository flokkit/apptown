package workflow

import (
	"context"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
)

// A change's branch is held by the run working on it
// (stock/docs/guides/changes-and-main.md, "One worker at a time"). Starting a
// root run hands the branch to it, and a replaced step attempt advances the
// authority, so an earlier attempt or run can no longer publish.
func runHolder(rootRunID ids.ID) string { return scm.RunHolder(rootRunID) }

// takeChangeForRunIn makes a new writing root run the holder of its change's
// branch, refusing while another holder has not ended.
func takeChangeForRunIn(ctx context.Context, tx *database.Tx, run RunRef, chats Presence, at time.Time) error {
	source, err := ReadSourceIn(ctx, tx, run)
	if err != nil || !source.RepoBacked() || source.Branch == "" {
		return err
	}
	_, err = TakeChangeIn(ctx, tx, run.ProjectID, source.Branch, runHolder(source.RootRunID), chats, at)
	return err
}

// landBesideHolderIn admits a landing, which takes no holder: beside the
// change's chat (whose committed work was published at admission) or with
// no holder, never while a writing run or a desktop checkout holds it.
func landBesideHolderIn(ctx context.Context, tx *database.Tx, run RunRef, chats Presence, at time.Time) error {
	source, err := ReadSourceIn(ctx, tx, run)
	if err != nil || !source.RepoBacked() || source.Branch == "" {
		return err
	}
	current, err := scm.LockAuthorityIn(ctx, tx, run.ProjectID, source.Branch)
	if err != nil || strings.HasPrefix(current.Holder, "chat:") {
		return err
	}
	ended, err := HolderEndedIn(ctx, tx, run.ProjectID, source.Branch, current.Holder, chats, at)
	if err == nil && !ended {
		err = &ChangeHeld{Holder: current.Holder}
	}
	return err
}

// claimSourceAuthorityIn returns the branch authority a starting attempt is
// admitted under. The attempt's root run must still hold the branch, and a
// retry advances the generation, revoking the attempt it replaces and any
// sibling still publishing under the old one. A landing holds nothing and
// publishes nothing: its attempts are admitted under no grant.
func claimSourceAuthorityIn(ctx context.Context, tx *database.Tx, run *Run, retry bool) (int64, error) {
	source, err := ReadSourceIn(ctx, tx, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return 0, err
	}
	if !source.RepoBacked() || source.Branch == "" || source.RootRole == "landing" {
		return 0, nil
	}
	authority, err := scm.ClaimAuthorityIn(ctx, tx, run.ProjectID, source.Branch, runHolder(source.RootRunID), retry)
	return authority.Generation, err
}
