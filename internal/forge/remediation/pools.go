package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/ids"
)

// Pools is the declared size of each worker pool (remediation.md §3): the
// remediation pool today, keyed by kind so review and import pools can
// follow. Zero pauses a kind entirely. The default for a kind never set is
// the caller's rule, not the table's.
type Pools struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func (p *Pools) now() time.Time { return clock.Or(p.clock)() }

func NewPools(db *database.DB, now func() time.Time) *Pools { return &Pools{db: db, clock: now} }

const MaxWorkers = 32

var ErrPoolSize = errors.New("worker count must be between 0 and 32")

// Count is a pool's declared size and whether one was ever declared.
func (p *Pools) Count(ctx context.Context, kind string) (int, bool, error) {
	row, err := queries(p.db).PoolByKind(ctx, kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil || row.Count < 0 {
		return 0, false, err
	}
	return int(row.Count), true, nil
}

// Set declares a pool's size, with the receipt in the same transaction.
func (p *Pools) Set(ctx context.Context, kind string, n int, who string) error {
	now := p.now()
	if n < 0 || n > MaxWorkers {
		return fmt.Errorf("%w: %d", ErrPoolSize, n)
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
