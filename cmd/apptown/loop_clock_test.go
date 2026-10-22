package main

import (
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A step AppTown gave no times is timed on the CLI's clock, from when the
// loop saw it running to when it saw it pass.
func TestAStepWithoutAppTownsTimesIsTimedOnTheCLIsClock(t *testing.T) {
	pollEvery = 0
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	real := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = real })
	polls := 0
	fakeAppTown(t, func(_, path string) (int, string) {
		polls++
		if polls == 1 {
			return 200, `{"run":{"id":"00000000-002c-7000-8000-000000000000","status":"running","currentStage":"build"},"steps":[{"id":"00000000-0001-7000-8000-000000000000","stage":"build","name":"apply","type":"script","status":"running"}]}`
		}
		at = at.Add(95 * time.Second) // the step ran 95 s on the CLI's clock
		return 200, `{"run":{"id":"00000000-002c-7000-8000-000000000000","status":"done","currentStage":"build"},"steps":[{"id":"00000000-0001-7000-8000-000000000000","stage":"build","name":"apply","type":"script","status":"pass"}]}`
	})
	out := capture(t, func() { _, _ = streamRunFor(idstest.N(44), ids.Nil, true) })
	if !strings.Contains(out, "apply 95s") {
		t.Fatalf("a step that ran 95 s on the CLI's clock:\n%s", out)
	}
}

// A run's heartbeat comes when the CLI's clock says it has been quiet long
// enough, however little real time passed.
func TestARunsHeartbeatIsDueByTheCLIsClock(t *testing.T) {
	pollEvery = 0
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	real := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = real })
	polls := 0
	fakeAppTown(t, func(_, path string) (int, string) {
		polls++
		if polls < 3 {
			at = at.Add(40 * time.Second) // quiet for longer than heartbeatEvery, on the CLI's clock
			return 200, `{"run":{"id":"00000000-002d-7000-8000-000000000000","status":"running","currentStage":"ship"},"state":"working","stateDetail":"deploy","steps":[{"id":"00000000-0001-7000-8000-000000000000","stage":"ship","name":"deploy","type":"deploy","status":"running","startedAt":"2030-12-31T23:59:00Z"}]}`
		}
		return 200, `{"run":{"id":"00000000-002d-7000-8000-000000000000","status":"done","currentStage":"ship"},"steps":[]}`
	})
	out := capture(t, func() { _, _ = streamRunFor(idstest.N(45), ids.Nil, true) })
	if !strings.Contains(out, "deploy still running") {
		t.Fatalf("no heartbeat after 80 s of quiet on the CLI's clock:\n%s", out)
	}
}
