package remediation

import (
	"context"
	"slices"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/stamp"
)

// A job ID selects its items; nil selects all worker holders for occupancy.
// Activity uses the same priority as before, and stays within the item's project.
func readItems(ctx context.Context, db database.Queryer, jobID *ids.ID) ([]Item, error) {
	var wanted ids.ID // none: every active item
	if jobID != nil {
		wanted = *jobID
	}
	rows, err := queries(db).ItemsForPage(ctx, wanted)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		it := item(row.RemediationItem)
		it.ChangeNumber, it.RunNumber = row.ChangeNumber, row.RunNumber
		items = append(items, it)
	}
	refs := make([]workflow.RunRef, 0, len(items))
	active := func(item Item) bool {
		return !item.RunID.IsZero() && slices.Contains([]string{ItemWorking, ItemAccepting, ItemLanding}, item.State)
	}
	for _, item := range items {
		if active(item) {
			refs = append(refs, workflow.RunRef{ID: item.RunID, ProjectID: item.ProjectID})
		}
	}
	steps, err := workflow.PendingStepsIn(ctx, db, refs)
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		if !active(item) {
			continue
		}
		if step, ok := steps[workflow.RunRef{ID: item.RunID, ProjectID: item.ProjectID}]; ok {
			items[i].Doing = activityDescription(step)
		}
	}
	return items, nil
}

// Keep entries for attempted items even when their history is absent. Unattempted
// items have no history entry. All histories for the job are read in one statement.
func readAttempts(ctx context.Context, db database.Queryer, jobID ids.ID) (map[ids.ID][]Attempt, error) {
	rows, err := queries(db).AttemptsForJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := map[ids.ID][]Attempt{}
	for _, r := range rows {
		if _, ok := out[r.ItemID]; !ok {
			out[r.ItemID] = nil
		}
		if r.ID.IsZero() {
			continue
		}
		out[r.ItemID] = append(out[r.ItemID], Attempt{
			ID: r.ID, ItemID: r.ItemID, N: int(r.N), ChangeID: r.ChangeID, RunID: r.RunID,
			BaseSHA: r.BaseSha, Outcome: r.Outcome, Reason: r.Reason,
			StartedAt:  stamp.Time{NullTime: r.StartedAt}.Time.UTC().Format(time.RFC3339Nano),
			FinishedAt: stamp.Time{NullTime: r.FinishedAt}, CostMicros: r.CostMicros,
		})
	}
	return out, nil
}

func readOccupancy(ctx context.Context, db database.Queryer) (Occupancy, error) {
	items, err := readItems(ctx, db, nil)
	if err != nil {
		return Occupancy{}, err
	}
	o := Occupancy{Kind: WorkerKindRemediation, Holding: items, Busy: len(items)}
	depth, err := queries(db).PoolAndQueueDepth(ctx, remediationsql.PoolAndQueueDepthParams{
		Kind: WorkerKindRemediation, Column2: int64(DefaultWorkers),
	})
	if err != nil {
		return Occupancy{}, err
	}
	o.Workers, o.Queued = int(depth.Declared), int(depth.Queued)
	return o, nil
}
