package database

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// ErrHeldElsewhere: another session holds the exclusive lock asked for.
var ErrHeldElsewhere = errors.New("held by another session")

// HoldExclusive takes a Postgres session-level advisory lock on this
// database under key and keeps the one connection that holds it until
// release is called (or the process ends). It never waits: a lock another
// session holds is ErrHeldElsewhere at once. The lock belongs to that
// connection, so a lost connection (a Postgres restart, a network cut)
// releases it without telling the holder; callers name that as accepted.
func (d *DB) HoldExclusive(ctx context.Context, key int64) (release func(), err error) {
	conn, err := d.raw.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("database: a connection for the exclusive lock: %w", err)
	}
	var held bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&held); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("database: taking the exclusive lock: %w", err)
	}
	if !held {
		_ = conn.Close()
		return nil, ErrHeldElsewhere
	}
	// Releasing must finish even after the caller's context ends.
	detached := context.WithoutCancel(ctx)
	return func() {
		// Unlock, then give the connection back to the pool. Giving it back
		// does not end its session, so a failed unlock leaves the lock held
		// until the pool closes that connection or the process ends: the
		// release is then unconfirmed, and the log says so. (forged holds the
		// lock for its whole life and never calls release.)
		if _, err := conn.ExecContext(detached, `SELECT pg_advisory_unlock($1)`, key); err != nil {
			log.Printf("database: the exclusive lock may still be held: unlocking failed: %v", err)
		}
		_ = conn.Close()
	}, nil
}
