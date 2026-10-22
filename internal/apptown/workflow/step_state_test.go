package workflow

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStepStateLifecycle(t *testing.T) {
	t.Parallel()
	// An explicit edge list is the contract. In particular, a running attempt
	// cannot be reset, voted on, overridden or replaced by a fresh evaluation.
	type edge struct {
		move     StepMove
		from, to StepState
	}
	edges := map[edge]bool{}
	allow := func(move StepMove, from StepState, to ...StepState) {
		for _, target := range to {
			edges[edge{move, from, target}] = true
		}
	}
	allow(MoveStepStart, StatusPending, StatusRunning)
	allow(MoveStepFinish, StatusRunning, StatusPass, StatusFail, StatusNeedsHuman, StatusError, StatusSkipped)
	allow(MoveStepVote, StatusPending, StatusPending, StatusPass, StatusFail)
	allow(MoveStepVote, StatusNeedsHuman, StatusNeedsHuman, StatusPass, StatusFail)
	allow(MoveStepReopen, StatusFail, StatusPending)
	for _, from := range []StepState{StatusPending, StatusFail, StatusNeedsHuman, StatusError, StatusSkipped} {
		allow(MoveStepOverride, from, StatusPass)
	}
	for _, from := range []StepState{StatusPending, StatusPass, StatusFail, StatusNeedsHuman, StatusError, StatusSkipped} {
		allow(MoveStepReset, from, StatusPending)
		allow(MoveStepEvaluate, from, StatusPass, StatusFail, StatusNeedsHuman, StatusError)
	}
	allow(MoveStepWithdraw, StatusPending, StatusFail)
	allow(MoveStepWithdraw, StatusNeedsHuman, StatusFail)
	allow(MoveStepSkip, StatusPending, StatusSkipped)
	allow(MoveStepSkip, StatusError, StatusSkipped)
	states := []StepState{0, StatusPending, StatusRunning, StatusPass, StatusFail, StatusNeedsHuman, StatusError, StatusSkipped, 255}
	for move := StepMove(0); move <= MoveStepSkip+1; move++ {
		for _, from := range states {
			for _, to := range states {
				next, err := from.Transition(move, to)
				if edges[edge{move, from, to}] {
					if err != nil || next != to {
						t.Fatalf("%d %s -> %s: %s, %v", move, from, to, next, err)
					}
				} else if err == nil || next != 0 {
					t.Fatalf("invalid edge %d %s -> %s admitted: %s, %v", move, from, to, next, err)
				}
			}
		}
	}
	for _, state := range states {
		if state.PassesGate() != (state == StatusPass || state == StatusSkipped) {
			t.Fatalf("gate admits %s", state)
		}
	}
}

func TestStepStateEncodingIsStrict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state StepState
		text  string
	}{
		{StatusPending, "pending"}, {StatusRunning, "running"}, {StatusPass, "pass"}, {StatusFail, "fail"}, {StatusNeedsHuman, "needs_human"}, {StatusError, "error"}, {StatusSkipped, "skipped"},
	} {
		data, err := json.Marshal(tc.state)
		if err != nil || string(data) != `"`+tc.text+`"` {
			t.Fatalf("JSON: %s, %v", data, err)
		}
		var got StepState
		if err := json.Unmarshal(data, &got); err != nil || got != tc.state {
			t.Fatalf("roundtrip: %s, %v", got, err)
		}
		value, err := tc.state.Value()
		if err != nil || value != tc.text {
			t.Fatalf("database value: %v, %v", value, err)
		}
		for _, value := range []any{tc.text, []byte(tc.text)} {
			if err := got.Scan(value); err != nil || got != tc.state {
				t.Fatalf("scan: %s, %v", got, err)
			}
		}
	}
	for _, raw := range []string{`""`, `"done"`, `"PASS"`, `" pass "`, `null`, `3`, `{}`} {
		got := StatusPass
		if err := json.Unmarshal([]byte(raw), &got); err == nil || got != StatusPass {
			t.Fatalf("invalid %s changed state: %s, %v", raw, got, err)
		}
	}
	for _, value := range []any{nil, 3, "done", []byte("")} {
		got := StatusPass
		if err := got.Scan(value); !errors.Is(err, errStepState) || got != StatusPass {
			t.Fatalf("invalid %v changed state: %s, %v", value, got, err)
		}
	}
	for _, state := range []StepState{0, 255} {
		if _, err := json.Marshal(state); !errors.Is(err, errStepState) {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, err := state.Value(); !errors.Is(err, errStepState) {
			t.Fatalf("invalid database state: %v", err)
		}
	}
}
