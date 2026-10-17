package conformance

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestReviewLifecycleIsTerminal(t *testing.T) {
	t.Parallel()
	all := []ReviewState{0, ReviewQueued, ReviewRunning, ReviewDone, ReviewError}
	for _, from := range all {
		for _, to := range all {
			next, err := from.Transition(to)
			legal := from == ReviewRunning && (to == ReviewDone || to == ReviewError) ||
				from == ReviewQueued && (to == ReviewRunning || to == ReviewError)
			if legal && (err != nil || next != to) {
				t.Fatalf("legal transition %s -> %s: %s %v", from, to, next, err)
			}
			if !legal && (err == nil || next != 0) {
				t.Fatalf("illegal transition %s -> %s returned state: %s %v", from, to, next, err)
			}
		}
	}
	for _, s := range []ReviewState{ReviewQueued, ReviewDone, ReviewError} {
		if err := s.RequireRunning(); !errors.Is(err, ErrReviewClosed) {
			t.Fatalf("terminal review admitted work: %s %v", s, err)
		}
	}
}

func TestReviewStateOwnsItsRepresentations(t *testing.T) {
	t.Parallel()
	for _, state := range []ReviewState{ReviewQueued, ReviewRunning, ReviewDone, ReviewError} {
		dbValue, err := state.Value()
		if err != nil || dbValue != state.String() {
			t.Fatalf("SQL representation: %v %v", dbValue, err)
		}
		wire, err := json.Marshal(state)
		if err != nil || string(wire) != `"`+state.String()+`"` {
			t.Fatalf("JSON representation: %s %v", wire, err)
		}
		for _, source := range []any{dbValue, []byte(state.String())} {
			var got ReviewState
			if err := got.Scan(source); err != nil || got != state {
				t.Fatalf("database state %v: %s %v", source, got, err)
			}
		}
		var got ReviewState
		if err := json.Unmarshal(wire, &got); err != nil || got != state {
			t.Fatalf("wire state %s: %s %v", wire, got, err)
		}
	}
	for _, bad := range []any{nil, "", "paused", "stale", "RUNNING", 1, []byte("unknown")} {
		got := ReviewDone
		if err := got.Scan(bad); !errors.Is(err, ErrReviewState) || got != ReviewDone {
			t.Fatalf("invalid database state changed a record: %v -> %s %v", bad, got, err)
		}
	}
	for _, bad := range []string{`null`, `""`, `"stale"`, `1`, `{}`} {
		got := ReviewDone
		if err := json.Unmarshal([]byte(bad), &got); err == nil || got != ReviewDone {
			t.Fatalf("invalid wire state changed a record: %s -> %s %v", bad, got, err)
		}
	}
	if _, err := ReviewState(0).Value(); !errors.Is(err, ErrReviewState) {
		t.Fatalf("uninitialized state accepted for storage: %v", err)
	}
	if _, err := json.Marshal(ReviewState(0)); !errors.Is(err, ErrReviewState) {
		t.Fatalf("uninitialized state accepted on wire: %v", err)
	}
}
