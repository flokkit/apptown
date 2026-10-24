package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/poll"
)

// Another suite's sweep keeps a suite's template and clones while the
// suite's owner connection is open, and drops them once it has gone. The
// sweep reads no process state: a binary in a container shares the private
// server with binaries on the host, and its process id means nothing here.
// What the server sees when an owning process exits or is killed, on the
// host or in a container, is its owner connection closing; the test closes
// it the same way.
func TestSweepKeepsASuiteWhileItsOwnerIsConnected(t *testing.T) {
	open := func(_ context.Context, url string) (*database.DB, error) { return database.Open(url) }
	owning := NewSuite(open)
	owning.once.Do(owning.setup)
	if owning.err != nil {
		t.Fatal(owning.err)
	}
	// Registered before New's own cleanup, so it runs after the clone's drop.
	t.Cleanup(func() {
		if err := owning.Close(); err != nil {
			t.Error(err)
		}
		if err := owning.admin.Close(); err != nil {
			t.Error(err)
		}
	})
	if owning.params == "" {
		t.Skip("APPTOWN_TEST_DB_URL names a shared server, which is never swept")
	}
	clone := owning.New(t)
	var cloneName string
	if err := clone.QueryRow(`SELECT current_database()`).Scan(&cloneName); err != nil {
		t.Fatal(err)
	}
	owned := []string{owning.template, cloneName}
	present := func(name string) bool {
		var n int
		if err := owning.admin.QueryRow(`SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	// The sweeping suite's setup sweeps, as every test binary's does.
	sweeping := NewSuite(open)
	sweeping.once.Do(sweeping.setup)
	t.Cleanup(func() {
		if err := sweeping.Close(); err != nil {
			t.Error(err)
		}
		if err := sweeping.admin.Close(); err != nil {
			t.Error(err)
		}
	})
	if sweeping.err != nil {
		t.Fatal(sweeping.err)
	}
	for _, name := range owned {
		if !present(name) {
			t.Fatalf("%s was swept while its owner was connected", name)
		}
	}

	ctx := context.Background()
	template := owning.template
	if err := owning.owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	owning.owner = nil
	// The server ends a closed connection's backend on its own time.
	var left int
	if !poll.Until(ctx, 30*time.Second, 10*time.Millisecond, func() bool {
		return owning.admin.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE application_name = $1`, template).Scan(&left) == nil && left == 0
	}) {
		t.Fatalf("the closed owner's backend never ended (%d left)", left)
	}
	sweeping.sweepOrphans()
	for _, name := range owned {
		if present(name) {
			t.Errorf("%s survived the sweep after its owner disconnected", name)
		}
	}
}
