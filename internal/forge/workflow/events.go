package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// Exact extension payloads have already been encoded by their owning domain.
// Every required Work receipt checks that the database accepted one row.
// at is AppTown's time of the event, from the recording owner's clock.
func recordEvent(ctx context.Context, db database.Queryer, runID ids.ID, kind string, payload json.RawMessage, at time.Time) error {
	if !json.Valid(payload) {
		return fmt.Errorf("invalid %s receipt", kind)
	}
	if at.IsZero() {
		return fmt.Errorf("workflow event %q needs AppTown's time", kind)
	}
	n, err := workflowsql.New(db.SQL()).InsertRunEvent(ctx, workflowsql.InsertRunEventParams{ID: ids.New(at), RunID: runID, At: at, Kind: kind, PayloadJson: payload})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("workflow event %q for run %d was not recorded", kind, runID)
	}
	return nil
}

func recordReceiptIn(ctx context.Context, tx *database.Tx, runID ids.ID, kind string, value any, at time.Time) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return recordEventIn(ctx, tx, runID, kind, body, at)
}

func recordEventIn(ctx context.Context, tx *database.Tx, runID ids.ID, kind string, payload json.RawMessage, at time.Time) error {
	return recordEvent(ctx, tx, runID, kind, payload, at)
}

// Notice is narration supplied by an execution adapter; it grants no
// transition. At is AppTown's time of it, from the adapter's clock.
// Required lifecycle receipts are written privately by their owning command.
type Notice struct {
	RunID   ids.ID
	Kind    string
	Payload json.RawMessage
	At      time.Time
}

func RecordNotice(ctx context.Context, db *database.DB, in Notice) error {
	if in.RunID.IsZero() || in.Kind == "" {
		return fmt.Errorf("a Work notice requires its run and kind")
	}
	return recordEvent(ctx, db, in.RunID, in.Kind, in.Payload, in.At)
}

// RecordNoticeOrLog records narration whose failure must not fail its
// caller: an unencodable payload or a refused write is logged, not returned.
// at is AppTown's time of it.
func RecordNoticeOrLog(ctx context.Context, db *database.DB, runID ids.ID, kind string, payload any, at time.Time) {
	body, err := json.Marshal(payload)
	if err == nil {
		err = RecordNotice(ctx, db, Notice{RunID: runID, Kind: kind, Payload: body, At: at})
	}
	if err != nil {
		log.Printf("workflow event %s for run %d: %v", kind, runID, err)
	}
}

// RecordNoticeOnce records a notice unless the run already has one of its
// kind that same reports as the same, for narration a replay would
// otherwise repeat (a task result finished after a restart).
func RecordNoticeOnce(ctx context.Context, db *database.DB, in Notice, same func(json.RawMessage) bool) error {
	if in.RunID.IsZero() || in.Kind == "" {
		return fmt.Errorf("a Work notice requires its run and kind")
	}
	rows, err := workflowsql.New(db.SQL()).ReadRunEvents(ctx, workflowsql.ReadRunEventsParams{RunID: in.RunID})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Kind == in.Kind && same(json.RawMessage(row.PayloadJson)) {
			return nil
		}
	}
	return recordEvent(ctx, db, in.RunID, in.Kind, in.Payload, in.At)
}
