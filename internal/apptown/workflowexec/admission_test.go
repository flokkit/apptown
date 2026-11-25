package workflowexec

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
)

type waitingScriptDriver struct {
	execution.TaskDriver
	admit func(string) (int, bool, error)
}

func (d waitingScriptDriver) Admit(_ context.Context, key string) (int, bool, error) {
	return d.admit(key)
}

func TestScriptAdmissionPrecedesReservationAndExecutionDeadlines(t *testing.T) {
	t.Parallel()
	a, fake, step := scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "timeoutMinutes": 1})
	a.onWorker(t, fake)
	// Whole seconds: the database keeps times to the microsecond.
	now := clock.Or(nil)().Truncate(time.Second)
	a.tasks.Now = func() time.Time { return now }
	a.commands.clock = a.tasks.Now
	a.tasks.Records.Now = a.tasks.Now
	a.tasks.AbandonAt = func(phase string) bool { return phase == "reserved" }
	asks := 0
	original := a.tasks.Driver
	a.tasks.Driver = waitingScriptDriver{TaskDriver: original, admit: func(key string) (int, bool, error) {
		asks++
		if _, err := a.tasks.Records.Get(a.ctx, key); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("task reserved before admission: %v", err)
		}
		now = now.Add(2 * time.Minute)
		if asks == 1 {
			return 3, false, nil
		}
		return original.Admit(a.ctx, key)
	}}
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	if asks != 2 {
		t.Fatalf("script bypassed waiting: %d admission requests", asks)
	}
	attempt, err := a.tasks.Records.Get(a.ctx, taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	if !attempt.Deadline.Equal(now.Add(time.Minute)) || !attempt.Admitted.Expires.Equal(now.Add(time.Minute+taskGrace)) {
		t.Fatalf("wait spent execution budget: %+v", attempt)
	}
	snapshot, err := a.work.Snapshot(a.ctx, step.RunID, workflow.TraceView)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range snapshot.Events {
		if event.Kind == "step.queued" {
			var payload struct {
				Ms int64 `json:"ms"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Ms != (4 * time.Minute).Milliseconds() {
				t.Fatalf("wait receipt: %s", event.Payload)
			}
			return
		}
	}
	t.Fatal("no step.queued receipt")
}
