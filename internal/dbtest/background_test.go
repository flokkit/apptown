package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
)

// A suite run through Run drops its clones after their tests finish, off the
// tests' own time. Close waits for those drops: once it returns, no clone and
// no template of the suite is left on the server, and a failed drop is its error.
func TestBackgroundDropsAreFinishedAndFailuresReportedByClose(t *testing.T) {
	suite := NewSuite(func(_ context.Context, url string) (*database.DB, error) { return database.Open(url) })
	suite.background = true // what Run sets before any test starts
	t.Run("clones", func(t *testing.T) {
		for _, name := range []string{"one", "two", "three", "four"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				if err := suite.New(t).Ping(); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	prefix := suite.template
	// A drop that fails in the background is reported by Close, never lost:
	// a database the server refuses to drop stays behind and fills the disk.
	suite.drops <- `"no such database" CASCADE`
	err := suite.Close()
	if err == nil || !strings.Contains(err.Error(), "no such database") {
		t.Fatalf("close must report the failed drop, got %v", err)
	}
	admin, err := sql.Open("pgx", suite.adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var left int
	if err := admin.QueryRow(`SELECT count(*) FROM pg_database WHERE datname LIKE $1`, prefix+"%").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d databases of the suite remain after Close", left)
	}
}

// Finished tests' clones wait for a drop only a few deep. When drops stall,
// the next finished test waits in its cleanup rather than leaving its clone
// on the server's small tmpfs behind an ever longer queue; once drops resume,
// every clone is dropped.
func TestFinishedTestsWaitWhenDropsFallBehind(t *testing.T) {
	suite := NewSuite(func(_ context.Context, url string) (*database.DB, error) { return database.Open(url) })
	suite.background = true // what Run sets before any test starts
	suite.once.Do(suite.setup)
	if suite.err != nil {
		t.Fatal(suite.err)
	}
	// A transaction that has commented on a database holds a lock on it that
	// DROP DATABASE waits for: the drops of these clones stall until it ends.
	holder, err := sql.Open("pgx", suite.adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	hold, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// More finished tests than a short drop queue can hold, chosen apart from
	// its depth: with every drop held, some of them must still be waiting.
	const tests = 8
	finished, cleaning := make(chan struct{}), make(chan int, tests)
	var released atomic.Bool
	go func() {
		defer close(finished)
		t.Run("clones", func(t *testing.T) {
			for i := range tests {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					var name string
					if err := suite.New(t).QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
						t.Fatal(err)
					}
					// Registered after New's, so it runs just before New's cleanup.
					t.Cleanup(func() { cleaning <- i })
					if released.Load() {
						return // the tests after the stall run with drops free
					}
					if _, err := hold.Exec(fmt.Sprintf(`COMMENT ON DATABASE %s IS 'held'`, name)); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}()
	// Each test's cleanup gets a bounded chance to hand its clone over; the
	// tests stop advancing at one whose cleanup is waiting for a drop.
	last := -1
	for stalled := false; !stalled; {
		select {
		case last = <-cleaning:
		case <-finished:
			t.Fatalf("all %d tests finished while none of their clones could be dropped", tests)
		case <-time.After(300 * time.Millisecond):
			stalled = true
		}
	}
	if last < 0 || last == tests-1 {
		t.Fatalf("the tests stopped at %d of %d; one should wait while drops are held", last, tests)
	}
	select {
	case <-finished:
		t.Fatalf("all %d tests finished while none of their clones could be dropped", tests)
	default:
	}
	released.Store(true)
	if err := hold.Rollback(); err != nil {
		t.Fatal(err)
	}
	<-finished
	prefix := suite.template
	if err := suite.Close(); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := suite.admin.QueryRow(`SELECT count(*) FROM pg_database WHERE datname LIKE $1`, prefix+"%").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d databases of the suite remain after Close", left)
	}
}
