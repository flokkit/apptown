package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

func TestRunStateLifecycle(t *testing.T) {
	t.Parallel()
	states := []RunState{0, RunRunning, RunDone, RunAbandoned, 255}
	for _, from := range states {
		for _, to := range states {
			got, err := from.Transition(to)
			allowed := from == RunRunning && (to == RunRunning || to == RunDone || to == RunAbandoned)
			if allowed {
				if err != nil || got != to {
					t.Fatalf("%s -> %s: %s, %v", from, to, got, err)
				}
			} else if err == nil || got != 0 {
				t.Fatalf("invalid transition %s -> %s admitted: %s, %v", from, to, got, err)
			}
		}
	}
}

func TestRunStateRejectsInvalidStoredRecords(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	if n, err := workflowsql.New(db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: "failed", ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("store invalid run state: %d %v", n, err)
	}
	if got, err := ReadRunIn(context.Background(), db, run.ID); got != nil || !errors.Is(err, errRunState) {
		t.Fatalf("invalid stored state escaped: %+v, %v", got, err)
	}
}

func TestRunStateEncodingIsStrict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state RunState
		text  string
	}{{RunRunning, "running"}, {RunDone, "done"}, {RunAbandoned, "abandoned"}} {
		data, err := json.Marshal(tc.state)
		if err != nil || string(data) != `"`+tc.text+`"` {
			t.Fatalf("JSON: %s, %v", data, err)
		}
		var got RunState
		if err := json.Unmarshal(data, &got); err != nil || got != tc.state {
			t.Fatalf("roundtrip: %s, %v", got, err)
		}
		value, err := tc.state.Value()
		if err != nil || value != tc.text {
			t.Fatalf("database value: %v, %v", value, err)
		}
		if err := got.Scan([]byte(tc.text)); err != nil || got != tc.state {
			t.Fatalf("database scan: %s, %v", got, err)
		}
	}
	for _, raw := range []string{`""`, `"failed"`, `"DONE"`, `" running "`, `null`, `2`, `{}`} {
		got := RunDone
		if err := json.Unmarshal([]byte(raw), &got); err == nil || got != RunDone {
			t.Fatalf("invalid %s changed state: %s, %v", raw, got, err)
		}
	}
	for _, value := range []any{nil, 1, "failed", []byte("")} {
		got := RunDone
		if err := got.Scan(value); !errors.Is(err, errRunState) || got != RunDone {
			t.Fatalf("invalid %v changed state: %s, %v", value, got, err)
		}
	}
	for _, state := range []RunState{0, 255} {
		if _, err := json.Marshal(state); !errors.Is(err, errRunState) {
			t.Fatalf("invalid state JSON: %v", err)
		}
		if _, err := state.Value(); !errors.Is(err, errRunState) {
			t.Fatalf("invalid database state: %v", err)
		}
	}
}
