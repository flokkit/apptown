package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/poll"
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
	defer func() {
		if suite.admin == nil {
			return // setup failed before opening it
		}
		if err := suite.admin.Close(); err != nil {
			t.Error(err)
		}
	}()
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
	// Close leaves the admin pool open: Run's suite lives as long as the
	// binary. A test that makes its own suite closes the pool, or under -count
	// every run leaves its connections open until the server refuses clients.
	t.Cleanup(func() {
		if err := suite.admin.Close(); err != nil {
			t.Error(err)
		}
	})
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
					if err := suite.New(t).SQL().QueryRowContext(context.Background(), `SELECT current_database()`).Scan(&name); err != nil {
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
	// The tests stop at one whose cleanup is waiting to hand its clone over.
	// That wait is read from the goroutines themselves, not inferred from a
	// pause: making a clone forces a checkpoint, and on a loaded machine one
	// clone takes longer than any pause a test could choose. The deadline only
	// bounds a failure.
	var done bool
	if !poll.Until(context.Background(), time.Minute, 10*time.Millisecond, func() bool {
		select {
		case <-finished:
			done = true
			return true
		default:
			return cleanupWaitsToHandOver()
		}
	}) {
		t.Fatalf("no finished test waited to hand its clone over while drops were held")
	}
	if done {
		t.Fatalf("all %d tests finished while none of their clones could be dropped", tests)
	}
	last := -1
	for reported := true; reported; {
		select {
		case last = <-cleaning:
		default:
			reported = false
		}
	}
	if last < 0 || last == tests-1 {
		t.Fatalf("the tests stopped at %d of %d; one should wait while drops are held", last, tests)
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

// cleanupWaitsToHandOver reports whether a test's cleanup is blocked sending
// its clone to the drop queue: a goroutine in a channel send, running a test
// cleanup through NewWith's. A send from a goroutine the cleanup started does
// not run testing's cleanup, and the test's own cleaning channel never blocks.
func cleanupWaitsToHandOver() bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, g := range strings.Split(string(buf), "\n\n") {
		header, _, _ := strings.Cut(g, "\n")
		if strings.Contains(header, "[chan send") &&
			strings.Contains(g, "dbtest.(*Suite).NewWith.func") &&
			strings.Contains(g, "testing.(*common).runCleanup") {
			return true
		}
	}
	return false
}
