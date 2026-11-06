package workflow

import (
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
	"time"
)

func TestReapWorkspaceDecision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	old := now.Add(-3 * 24 * time.Hour)
	fresh := now.Add(-2 * time.Hour)
	cs := func(id ids.ID) (ChangeState, time.Time) {
		return map[int64]ChangeState{1: ChangeShipped, 2: ChangeInProgress, 3: ChangeAbandoned, 4: ChangeShipped}[int64(idstest.Of(id))],
			map[int64]time.Time{1: old, 2: time.Time{}, 3: old, 4: fresh}[int64(idstest.Of(id))]
	}
	rs := func(id ids.ID) (RunState, time.Time) {
		return map[int64]RunState{7: RunDone, 8: RunRunning, 9: RunAbandoned}[int64(idstest.Of(id))], map[int64]time.Time{7: old, 8: fresh, 9: fresh}[int64(idstest.Of(id))]
	}
	for entry, want := range map[string]bool{"change-00000000-0001-7000-8000-000000000000": true, "change-00000000-0002-7000-8000-000000000000": false, "change-00000000-0003-7000-8000-000000000000": true, "change-00000000-0004-7000-8000-000000000000": false, "change-00000000-0063-7000-8000-000000000000": false,
		"run-00000000-0007-7000-8000-000000000000": true, "run-00000000-0008-7000-8000-000000000000": false, "run-00000000-0009-7000-8000-000000000000": false, "project-quick-poll": false, "junk": false} {
		if got, _ := reapWorkspaceDecision(entry, now, cs, rs); got != want {
			t.Errorf("%s: reap=%v want %v", entry, got, want)
		}
	}
}
