package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// pools is the declared size of each worker pool (remediation.md §3): the
// remediation pool today, keyed by kind so review and import pools can
// follow. Zero pauses a kind entirely. The default for a kind never set is
// the caller's rule, not the table's.
type pools struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func (p *pools) now() time.Time { return clock.Or(p.clock)() }

func newPools(db *database.DB, now func() time.Time) *pools { return &pools{db: db, clock: now} }

const maxWorkers = 32

var errPoolSize = errors.New("worker count must be between 0 and 32")

// count is a pool's declared size and whether one was ever declared.
func (p *pools) count(ctx context.Context, kind string) (int, bool, error) {
	row, err := queries(p.db).PoolByKind(ctx, kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil || row.Count < 0 {
		return 0, false, err
	}
	return int(row.Count), true, nil
}

// set declares a pool's size, with the receipt in the same transaction.
func (p *pools) set(ctx context.Context, kind string, n int, who string) error {
	now := p.now()
	if n < 0 || n > maxWorkers {
		return fmt.Errorf("%w: %d", errPoolSize, n)
	}
	return p.db.InTx(ctx, func(tx *database.Tx) error {
		if err := queries(tx).UpsertPool(ctx, remediationsql.UpsertPoolParams{
			Kind: kind, Count: int64(n), UpdatedAt: now.UTC(),
		}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "workers.set", kind, map[string]any{"count": n})
	})
}
