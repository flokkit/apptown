package changeactions_test

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/ids"
)

func readScanSnapshot(source attention.Scans, ctx context.Context, pid ids.ID) (attention.Snapshot, error) {
	rows, err := source.Snapshot(ctx, []ids.ID{pid})
	if err != nil {
		return attention.Snapshot{}, err
	}
	return rows[pid].Snapshot, rows[pid].Err
}
