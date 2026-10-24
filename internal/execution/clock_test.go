package execution_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// A task's admission expiry is judged on the driver's clock, the one that
// wrote it, not the real one: a task expired by that clock is refused
// though the real clock would admit it, and one it still admits goes on
// though the real clock has passed its expiry. Each driver refuses the
// admitted one for a later reason, before any effect.
func TestATasksExpiryIsJudgedOnTheDriversClock(t *testing.T) {
	t.Parallel()
	wall := time.Now()
	spec := func(expires time.Time) execution.TaskSpec {
		return execution.TaskSpec{Key: "k", Image: "busybox", Argv: []string{"true"},
			Admission: execution.Admission{Profile: "test", Network: execution.NetworkNone, Expires: expires}}
	}
	ahead := func() time.Time { return wall.Add(2 * time.Hour) }
	behind := func() time.Time { return wall.Add(-2 * time.Hour) }
	for _, c := range []struct {
		name   string
		ensure func(now func() time.Time, s execution.TaskSpec) error
		later  func(*execution.TaskSpec) // refused after the expiry is judged
	}{
		{"docker", func(now func() time.Time, s execution.TaskSpec) error {
			_, err := execution.DockerTasks{ToolVersions: stock.Versions(), Now: now}.Ensure(context.Background(), s)
			return err
		}, func(s *execution.TaskSpec) { s.SharedWorkspace = "shared" }},
		{"worker", func(now func() time.Time, s execution.TaskSpec) error {
			_, err := execution.WorkerTasks{URL: "https://worker.invalid", Now: now}.Ensure(context.Background(), s)
			return err
		}, func(s *execution.TaskSpec) { s.Workspace = "/work" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.ensure(ahead, spec(wall.Add(time.Hour))); !errors.Is(err, execution.ErrExpired) {
				t.Fatalf("a task expired by the driver's clock was not refused as expired: %v", err)
			}
			passed := spec(wall.Add(-time.Hour))
			c.later(&passed)
			if err := c.ensure(behind, passed); errors.Is(err, execution.ErrExpired) || !errors.Is(err, execution.ErrUnsupported) {
				t.Fatalf("a task the driver's clock admits was judged by another: %v", err)
			}
		})
	}
}
