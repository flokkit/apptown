package maintenance

import (
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestReapByAge(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := now.Add(-3 * time.Hour) // past the 2h TTL
	fresh := now.Add(-10 * time.Minute)

	// env is persistent — NEVER reaped, at any age. This is the safety invariant.
	if reapByAge("env", old, now) {
		t.Fatal("env must never be reaped by age")
	}
	// unknown/unlabelled → left alone.
	if reapByAge("", old, now) || reapByAge("infra", old, now) {
		t.Fatal("unknown classes must not be reaped")
	}
	// console is decided by idleness, not age.
	if reapByAge("console", old, now) {
		t.Fatal("console must not be reaped by age (idleness decides)")
	}
	// a preview is decided by the last look at it, which Preview keeps.
	if reapByAge("preview", old, now) {
		t.Fatal("a preview must not be reaped by age (the last look decides)")
	}
	// bounded-life ephemeral classes: reaped only when past the TTL.
	for _, class := range []string{"service", "step", "dind"} {
		if !reapByAge(class, old, now) {
			t.Fatalf("%s older than the TTL must be reapable", class)
		}
		if reapByAge(class, fresh, now) {
			t.Fatalf("%s within the TTL must be kept", class)
		}
		if reapByAge(class, time.Time{}, now) {
			t.Fatalf("%s with no created time must not be reaped (unknown age)", class)
		}
	}
}

func TestReapImageDecision(t *testing.T) {
	t.Parallel()
	now := time.Now()
	runs := map[int64]workflow.RunState{7: workflow.RunDone, 8: workflow.RunRunning}
	// 999 no longer exists (its project was deleted); 1000 could not be
	// read.
	rs := func(id ids.ID) ownerRecord[workflow.RunState] {
		if idstest.Of(id) == 1000 {
			return ownerRecord[workflow.RunState]{}
		}
		st, ok := runs[int64(idstest.Of(id))]
		return ownerRecord[workflow.RunState]{State: st, Gone: !ok, Known: true}
	}
	cases := []struct {
		ref     string
		created time.Time
		want    bool
	}{
		{"forge-preview-build-forge-preview-run-00000000-0007-7000-8000-000000000000:latest", now, true},
		{"forge-preview-build-forge-preview-run-00000000-0008-7000-8000-000000000000:latest", now, false},
		{"forge-preview-build-forge-preview-run-00000000-03e7-7000-8000-000000000000:latest", now, true},
		{"forge-preview-build-forge-preview-run-00000000-03e8-7000-8000-000000000000:latest", now, false},
		{"forge-preview-build-forge-preview-run-00000000-0007-7000-8000-000000000000-web:latest", now, true},
		{"forge-preview-build-forge-preview-run-00000000-0008-7000-8000-000000000000-web:latest", now, false},
		// A change's preview's build images are Preview's (the last look).
		{"forge-preview-build-forge-preview-change-00000000-0003-7000-8000-000000000000:latest", now.Add(-720 * time.Hour), false},
		{"forge-preview-build-forge-preview-change-00000000-0003-7000-8000-000000000000-init-db:latest", now.Add(-720 * time.Hour), false},
		{"forge-conform:abc123", now.Add(-2 * conformImageTTL), true},
		{"forge-conform:abc123", now, false},
		{"forge-preview-build-forge-env-quick-poll-main:latest", now.Add(-72 * time.Hour), false},
		{"apptown-agent:latest", now.Add(-72 * time.Hour), false},
		{"postgres:16-alpine", now.Add(-720 * time.Hour), false},
	}
	for _, c := range cases {
		if got, _ := reapImageDecision(c.ref, c.created, now, rs); got != c.want {
			t.Errorf("%s: reap=%v want %v", c.ref, got, c.want)
		}
	}
}

func TestReapCacheDecision(t *testing.T) {
	t.Parallel()
	rs := func(id ids.ID) workflow.RunState {
		return map[int64]workflow.RunState{7: workflow.RunDone, 8: workflow.RunRunning}[int64(idstest.Of(id))]
	}
	cs := func(id ids.ID) workflow.ChangeState {
		return map[int64]workflow.ChangeState{3: workflow.ChangeAbandoned, 4: workflow.ChangeLanding}[int64(idstest.Of(id))]
	}
	for entry, want := range map[string]bool{"change-00000000-0003-7000-8000-000000000000": true, "change-00000000-0004-7000-8000-000000000000": false, "change-00000000-0063-7000-8000-000000000000": false, "run-00000000-0007-7000-8000-000000000000": true, "run-00000000-0008-7000-8000-000000000000": false, "project-quick-poll": false, "asof-change-00000000-0003-7000-8000-000000000000": false} {
		if got, _ := reapCacheDecision(entry, rs, cs); got != want {
			t.Errorf("%s: reap=%v want %v", entry, got, want)
		}
	}
}

func TestReapWorkspaceDecision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	old := now.Add(-3 * 24 * time.Hour)
	fresh := now.Add(-2 * time.Hour)
	cs := func(id ids.ID) (workflow.ChangeState, time.Time) {
		return map[int64]workflow.ChangeState{1: workflow.ChangeShipped, 2: workflow.ChangeInProgress, 3: workflow.ChangeAbandoned, 4: workflow.ChangeShipped}[int64(idstest.Of(id))],
			map[int64]time.Time{1: old, 2: time.Time{}, 3: old, 4: fresh}[int64(idstest.Of(id))]
	}
	rs := func(id ids.ID) (workflow.RunState, time.Time) {
		return map[int64]workflow.RunState{7: workflow.RunDone, 8: workflow.RunRunning, 9: workflow.RunAbandoned}[int64(idstest.Of(id))], map[int64]time.Time{7: old, 8: fresh, 9: fresh}[int64(idstest.Of(id))]
	}
	for entry, want := range map[string]bool{"change-00000000-0001-7000-8000-000000000000": true, "change-00000000-0002-7000-8000-000000000000": false, "change-00000000-0003-7000-8000-000000000000": true, "change-00000000-0004-7000-8000-000000000000": false, "change-00000000-0063-7000-8000-000000000000": false,
		"run-00000000-0007-7000-8000-000000000000": true, "run-00000000-0008-7000-8000-000000000000": false, "run-00000000-0009-7000-8000-000000000000": false, "project-quick-poll": false, "junk": false} {
		if got, _ := reapWorkspaceDecision(entry, now, cs, rs); got != want {
			t.Errorf("%s: reap=%v want %v", entry, got, want)
		}
	}
}
