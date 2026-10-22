package workflow

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestChangeLifecycleActs(t *testing.T) {
	t.Parallel()
	states := []ChangeState{ChangeProposed, ChangeInProgress, ChangeLanding, ChangeShipped, ChangeAbandoned}
	// Each row is a business act; zero marks refusal. This table also protects
	// cancellation's no-op semantics and terminal Changes from accidental reopening.
	cases := []struct {
		act  ChangeAction
		next [5]ChangeState
	}{
		{StartChange, [5]ChangeState{ChangeInProgress, ChangeInProgress, 0, 0, 0}},
		{AttachChangeRun, [5]ChangeState{0, ChangeInProgress, ChangeLanding, 0, 0}},
		{LandChange, [5]ChangeState{0, ChangeLanding, ChangeLanding, 0, 0}},
		{ResumeChange, [5]ChangeState{0, ChangeInProgress, ChangeInProgress, 0, 0}},
		{CancelChangeLanding, [5]ChangeState{ChangeProposed, ChangeInProgress, ChangeInProgress, ChangeShipped, ChangeAbandoned}},
		{ShipChange, [5]ChangeState{0, ChangeShipped, ChangeShipped, 0, 0}},
		{AbandonChange, [5]ChangeState{ChangeAbandoned, ChangeAbandoned, ChangeAbandoned, 0, 0}},
		{255, [5]ChangeState{}},
	}
	for _, tc := range cases {
		for i, from := range states {
			got, err := from.Transition(tc.act)
			if got != tc.next[i] || (err != nil) != (tc.next[i] == 0) {
				t.Fatalf("act %d from %s: %s %v", tc.act, from, got, err)
			}
			if err != nil && !errors.Is(err, ErrChangeTransition) {
				t.Fatalf("untyped conflict: %v", err)
			}
		}
	}
	for _, state := range []ChangeState{0, 255} {
		for _, tc := range cases {
			if next, err := state.Transition(tc.act); next != 0 || !errors.Is(err, errChangeState) {
				t.Fatalf("invalid stored state acted: %s %v", next, err)
			}
		}
	}
}

func TestChangeStateEncodingAndAbsence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state ChangeState
		text  string
	}{{ChangeProposed, "proposed"}, {ChangeInProgress, "in_progress"}, {ChangeLanding, "landing"}, {ChangeShipped, "shipped"}, {ChangeAbandoned, "abandoned"}} {
		data, err := json.Marshal(tc.state)
		if err != nil || string(data) != `"`+tc.text+`"` {
			t.Fatalf("wire state %s: %s %v", tc.state, data, err)
		}
		var got ChangeState
		if err := json.Unmarshal(data, &got); err != nil || got != tc.state {
			t.Fatalf("roundtrip: %s %v", got, err)
		}
		value, err := tc.state.Value()
		if err != nil || value != tc.text {
			t.Fatalf("database state: %v %v", value, err)
		}
		if err := got.Scan([]byte(tc.text)); err != nil || got != tc.state {
			t.Fatalf("stored bytes: %s %v", got, err)
		}
	}
	for _, raw := range []string{`""`, `"running"`, `"SHIPPED"`, `" in_progress "`, `null`, `3`, `{}`} {
		got := ChangeShipped
		if err := json.Unmarshal([]byte(raw), &got); err == nil || got != ChangeShipped {
			t.Fatalf("invalid wire state %s changed record: %s %v", raw, got, err)
		}
	}
	for _, v := range []any{nil, 1, "running", []byte("")} {
		got := ChangeShipped
		if err := got.Scan(v); !errors.Is(err, errChangeState) || got != ChangeShipped {
			t.Fatalf("invalid stored %v: %s %v", v, got, err)
		}
	}
	for _, state := range []ChangeState{0, 255} {
		if _, err := json.Marshal(state); !errors.Is(err, errChangeState) {
			t.Fatalf("invalid JSON state: %v", err)
		}
		if _, err := state.Value(); !errors.Is(err, errChangeState) {
			t.Fatalf("invalid database state: %v", err)
		}
		if state.Closed() || state.CanRun() {
			t.Fatal("unknown observation admitted work or cleanup")
		}
	}
}
