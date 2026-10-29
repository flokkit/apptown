package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/poll"
)

// WaitForLock observes that the named statement is blocked in PostgreSQL.
// Tests use it to order concurrent operations without guessing a sleep duration.
func WaitForLock(t testing.TB, db *database.DB, query string) {
	t.Helper()
	var err error
	if poll.Until(context.Background(), 5*time.Second, 5*time.Millisecond, func() bool {
		var n int
		err = db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, "%"+query+"%").Scan(&n)
		return err != nil || n > 0
	}) {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("operation did not reach database lock: %s", query)
}
