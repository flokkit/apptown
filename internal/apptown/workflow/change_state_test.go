package workflow

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestChangeLifecycleActs(t *testing.T) {
	t.Parallel()
	states := []ChangeState{ChangeInProgress, ChangeLanding, ChangeShipped, ChangeAbandoned}
	// Each row is a business act; zero marks refusal. This table also protects
	// cancellation's no-op semantics and terminal Changes from accidental reopening.
	cases := []struct {
		act  ChangeAction
		next [4]ChangeState
	}{
		{attachChangeRun, [4]ChangeState{ChangeInProgress, ChangeLanding, 0, 0}},
		{LandChange, [4]ChangeState{ChangeLanding, ChangeLanding, 0, 0}},
		{resumeChange, [4]ChangeState{ChangeInProgress, ChangeInProgress, 0, 0}},
		{cancelChangeLanding, [4]ChangeState{ChangeInProgress, ChangeInProgress, ChangeShipped, ChangeAbandoned}},
		{ShipChange, [4]ChangeState{ChangeShipped, ChangeShipped, 0, 0}},
		{abandonChange, [4]ChangeState{ChangeAbandoned, ChangeAbandoned, 0, 0}},
		{255, [4]ChangeState{}},
	}
	for _, tc := range cases {
		for i, from := range states {
			got, err := from.transition(tc.act)
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
			if next, err := state.transition(tc.act); next != 0 || !errors.Is(err, errChangeState) {
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
	}{{ChangeInProgress, "in_progress"}, {ChangeLanding, "landing"}, {ChangeShipped, "shipped"}, {ChangeAbandoned, "abandoned"}} {
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
	for _, raw := range []string{`""`, `"proposed"`, `"running"`, `"SHIPPED"`, `" in_progress "`, `null`, `3`, `{}`} {
		got := ChangeShipped
		if err := json.Unmarshal([]byte(raw), &got); err == nil || got != ChangeShipped {
			t.Fatalf("invalid wire state %s changed record: %s %v", raw, got, err)
		}
	}
	for _, v := range []any{nil, 1, "running", "proposed", []byte("")} {
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
		if state.Closed() || state.canRun() {
			t.Fatal("unknown observation admitted work or cleanup")
		}
	}
}
