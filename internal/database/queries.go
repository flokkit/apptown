package database

import "github.com/flokkit/apptown/internal/database/query"

// Queryer supplies checked statements on its current pool or transaction.
// Generated queries use database/sql directly, on the same pool or transaction
// as handwritten queries. Keep failure injection on this boundary too, so
// migrating a write cannot silently bypass the atomicity tests.
type Queryer interface{ SQL() query.Handle }

func (d *DB) SQL() query.Handle {
	return query.Handle{Executor: d.raw, Check: d.faults.check, Error: QueryError}
}
func (t *Tx) SQL() query.Handle {
	return query.Handle{Executor: t.tx, Check: t.faults.check, Error: QueryError}
}
