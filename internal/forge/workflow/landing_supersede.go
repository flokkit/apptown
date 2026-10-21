package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// A landing whose merge SCM has recorded can no longer move main, so it no
// longer holds the project's landing slot (HasActiveLanding). A landing
// started beside such landings names them in its landing.passed receipt:
// their merges were on main before it started, so its own merge, made after
// it catches up with main, contains them. When it ships, it settles those
// still running as superseded. Without this, a landing stuck after its merge
// (its deploy refused) blocked the fix-forward that AppTown itself advises.

// ClosedShippedWith is Change.ClosedAs for a change shipped by a later
// landing that superseded its own; its landing.superseded receipt names that
// landing and its change.
const ClosedShippedWith = "shipped-with"

type passedLanding struct {
	Run    ids.ID `json:"run"`
	Change ids.ID `json:"change"`
	Merged string `json:"merged"` // the commit its merge recorded on main
}

type landingPassedReceipt struct {
	Landings []passedLanding `json:"landings"`
}

type landingSupersededReceipt struct {
	By       ids.ID `json:"by"`       // the landing that shipped main
	ByChange ids.ID `json:"byChange"` // its change
	Merged   string `json:"merged"`   // this landing's merge, on main before it started
}

type landingSupersedesReceipt struct {
	Landing ids.ID `json:"landing"`
	Change  ids.ID `json:"change"`
}

// mergedActiveLandingsIn lists the running landings a new landing of the
// project is admitted beside.
func mergedActiveLandingsIn(ctx context.Context, q *workflowsql.Queries, project ids.ID) ([]passedLanding, error) {
	rows, err := q.MergedActiveLandings(ctx, workflowsql.MergedActiveLandingsParams{ProjectID: project, Status: RunRunning.String()})
	if err != nil {
		return nil, err
	}
	out := make([]passedLanding, 0, len(rows))
	for _, r := range rows {
		out = append(out, passedLanding{Run: r.ID, Change: r.ChangeID, Merged: r.Result})
	}
	return out, nil
}

// supersedePassedIn settles, when landing ships its change, each landing its
// landing.passed receipt named that is still running: its run tree is
// abandoned as superseded and its change, still landing, ships with this one.
// Its failed steps and their evidence are kept.
func supersedePassedIn(ctx context.Context, tx *database.Tx, landing *Run, at time.Time) error {
	q := workflowsql.New(tx.SQL())
	row, err := q.LatestRunEventOfKind(ctx, workflowsql.LatestRunEventOfKindParams{RunID: landing.ID, Kind: "landing.passed"})
	if errors.Is(err, sql.ErrNoRows) {
		return nil // admitted with no merged landing beside it
	}
	if err != nil {
		return err
	}
	var passed landingPassedReceipt
	if err := json.Unmarshal(row.PayloadJson, &passed); err != nil {
		return fmt.Errorf("landing %s: landing.passed receipt: %w", landing.ID, err)
	}
	for _, p := range passed.Landings {
		if _, err := q.ScopedRunForUpdate(ctx, workflowsql.ScopedRunForUpdateParams{ID: p.Run, ProjectID: landing.ProjectID}); err != nil {
			return err
		}
		old, err := readScopedRun(ctx, tx, p.Run, landing.ProjectID)
		if err != nil {
			return err
		}
		if old.Status != RunRunning || old.ChangeID != p.Change {
			continue // finished or settled meanwhile: left alone
		}
		rows, err := q.AbandonActiveRunTree(ctx, workflowsql.AbandonActiveRunTreeParams{Status: RunRunning.String(), RootID: old.ID, ProjectID: landing.ProjectID})
		if err != nil {
			return err
		}
		reason := fmt.Sprintf("superseded by change %s's landing %s, which shipped main containing this landing's merge %s", landing.ChangeID, landing.ID, p.Merged)
		for _, row := range rows {
			run, err := runRecord(row, nil)
			if err != nil {
				return err
			}
			if err := TransitionRunIn(ctx, tx, run, RunAbandoned, run.CurrentStage, at); err != nil {
				return err
			}
			if err := recordReceiptIn(ctx, tx, run.ID, "run.abandoned", abandonedRunReceipt{By: "apptown-system", Reason: reason, Root: old.ID}, at); err != nil {
				return err
			}
		}
		if err := recordReceiptIn(ctx, tx, old.ID, "landing.superseded", landingSupersededReceipt{By: landing.ID, ByChange: landing.ChangeID, Merged: p.Merged}, at); err != nil {
			return err
		}
		if _, err := TransitionChangeIn(ctx, tx, landing.ProjectID, p.Change, ChangeMove{Action: ShipChange}, at); err != nil {
			return fmt.Errorf("shipping superseded change %s: %w", p.Change, err)
		}
		n, err := q.MarkChangeClosedAs(ctx, workflowsql.MarkChangeClosedAsParams{ClosedAs: ClosedShippedWith, ID: p.Change, ProjectID: landing.ProjectID, State: ChangeShipped.String()})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("change %s: closed as shipped-with marked %d rows", p.Change, n)
		}
		if err := recordReceiptIn(ctx, tx, landing.ID, "landing.supersedes", landingSupersedesReceipt{Landing: old.ID, Change: p.Change}, at); err != nil {
			return err
		}
	}
	return nil
}
