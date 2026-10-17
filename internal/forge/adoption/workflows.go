package adoption

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
)

// laneStore is a project's selection of the library's workflows.
type laneStore struct{ db *database.DB }

func newLanes(db *database.DB) *laneStore { return &laneStore{db: db} }

// Selected: the selection row decides; no row means not selected.
func (s *laneStore) Selected(ctx context.Context, projectID ids.ID, name string) (bool, error) {
	r, err := adoptionsql.New(s.db.SQL()).ProjectLane(ctx, adoptionsql.ProjectLaneParams{ProjectID: projectID, Name: name})
	if found, err := database.Lookup(err); err != nil || !found {
		return false, err
	}
	return r.Enabled, nil
}

// setLaneIn records a lane's selection at the caller's clock.
func setLaneIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name string, enabled bool, at time.Time) error {
	_, err := adoptionsql.New(tx.SQL()).SetProjectLane(ctx, adoptionsql.SetProjectLaneParams{ProjectID: projectID, Name: name, Enabled: enabled, At: at.UTC()})
	return err
}

// SelectOnlyIn makes exactly the named lanes a project's selection,
// inside the caller's transaction, at the caller's clock: every other
// selection is turned off.
func SelectOnlyIn(ctx context.Context, tx *database.Tx, projectID ids.ID, names []string, at time.Time) error {
	q := adoptionsql.New(tx.SQL())
	if _, err := q.DeselectProjectLanes(ctx, adoptionsql.DeselectProjectLanesParams{ProjectID: projectID, UpdatedAt: at.UTC()}); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := q.SetProjectLane(ctx, adoptionsql.SetProjectLaneParams{ProjectID: projectID, Name: name, Enabled: true, At: at.UTC()}); err != nil {
			return err
		}
	}
	return nil
}
