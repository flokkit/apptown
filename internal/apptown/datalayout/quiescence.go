package datalayout

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
)

// Writer observations are renewed on every restart; a saved journal is not
// evidence that an external runtime remained stopped while AppTown was down.
func refuseWriters(ctx context.Context, db *database.DB, root string, observers Observers) error {
	records, err := console.RelocationRecords(root)
	if err != nil {
		return err
	}
	for _, record := range records {
		if conversation.Imported(record.Meta.Labels) {
			continue
		}
		agent := observers.Local
		if conversation.OnWorkerRecord(record.Meta) {
			agent = observers.Remote
		}
		if err := record.RefuseWriter(ctx, agent); err != nil {
			return err
		}
	}
	requests, err := workflowexec.RelocationRequests(ctx, db, root)
	if err != nil {
		return err
	}
	for _, request := range requests {
		if err := request.RefuseWriter(ctx, observers.Tasks, observers.TaskBackend); err != nil {
			return err
		}
	}
	return nil
}
