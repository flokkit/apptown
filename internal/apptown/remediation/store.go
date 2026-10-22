package remediation

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// Readers return snapshots; admission and outcome stores retain their locks,
// eligibility checks and receipts. The queries also work in the caller's
// transaction, which is how admission reads what it is about to change.
func ReadItem(ctx context.Context, db database.Queryer, id ids.ID) (Item, error) {
	return readItem(ctx, db, id)
}

func readItem(ctx context.Context, db database.Queryer, id ids.ID) (Item, error) {
	row, err := queries(db).ItemByID(ctx, id)
	if err != nil {
		return Item{}, err
	}
	return item(row), nil
}

func readJob(ctx context.Context, db database.Queryer, id ids.ID) (*Remediation, error) {
	row, err := queries(db).RemediationSummary(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := remediationOf(row.Remediation, row.Counts, row.CostMicros)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func readJobs(ctx context.Context, db database.Queryer, limit int) ([]Remediation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := queries(db).RemediationSummaries(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]Remediation, 0, len(rows))
	for _, r := range rows {
		m, err := remediationOf(r.Remediation, r.Counts, r.CostMicros)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
