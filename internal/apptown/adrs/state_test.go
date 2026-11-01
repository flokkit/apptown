package adrs

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecisionLifecycle(t *testing.T) {
	t.Parallel()
	states := []State{Draft, proposed, Accepted, rejected, Deprecated, Superseded}
	cases := []struct {
		act  Decision
		next [6]State
	}{
		{redraft, [6]State{Draft, Draft, 0, Draft, 0, 0}},
		{Propose, [6]State{proposed, proposed, 0, 0, 0, 0}},
		{Accept, [6]State{Accepted, Accepted, Accepted, 0, Accepted, 0}},
		{reject, [6]State{rejected, rejected, 0, rejected, 0, 0}},
		{deprecate, [6]State{0, 0, Deprecated, 0, Deprecated, 0}},
		{retire, [6]State{0, 0, Superseded, 0, 0, Superseded}},
		{255, [6]State{}},
	}
	for _, c := range cases {
		for i, s := range states {
			got, err := s.decide(c.act)
			if got != c.next[i] || (err != nil) != (c.next[i] == 0) {
				t.Fatalf("act %d from %s: %s %v", c.act, s, got, err)
			}
		}
	}
	for _, s := range []State{0, 255} {
		for _, c := range cases {
			if got, err := s.decide(c.act); got != 0 || !errors.Is(err, errState) {
				t.Fatalf("invalid state acted: %s %v", got, err)
			}
		}
	}
	if act, err := ParseDecision("superseded"); act != 0 || !errors.Is(err, errTransition) {
		t.Fatalf("manual retirement admitted: %d %v", act, err)
	}
}
func TestStateEncodingIsStrict(t *testing.T) {
	t.Parallel()
	for _, s := range []State{Draft, proposed, Accepted, rejected, Deprecated, Superseded} {
		data, err := json.Marshal(s)
		if err != nil || string(data) != `"`+s.String()+`"` {
			t.Fatalf("wire state: %s %v", data, err)
		}
		var got State
		if err := json.Unmarshal(data, &got); err != nil || got != s {
			t.Fatalf("roundtrip: %s %v", got, err)
		}
		value, err := s.Value()
		if err != nil || value != s.String() {
			t.Fatalf("database value: %v %v", value, err)
		}
		if err := got.Scan([]byte(s.String())); err != nil || got != s {
			t.Fatalf("database bytes: %s %v", got, err)
		}
	}
	for _, raw := range []string{`null`, `""`, `" ACCEPTED "`, `"in_progress"`, `2`, `{}`} {
		got := Accepted
		if err := json.Unmarshal([]byte(raw), &got); err == nil || got != Accepted {
			t.Fatalf("invalid wire value changed record: %s %v", got, err)
		}
	}
	for _, raw := range []any{nil, 2, "unknown", []byte("")} {
		got := Accepted
		if err := got.Scan(raw); !errors.Is(err, errState) || got != Accepted {
			t.Fatalf("invalid stored value: %s %v", got, err)
		}
	}
	for _, s := range []State{0, 255} {
		if _, err := s.Value(); !errors.Is(err, errState) {
			t.Fatalf("invalid write: %v", err)
		}
		if _, err := json.Marshal(s); !errors.Is(err, errState) {
			t.Fatalf("invalid JSON: %v", err)
		}
	}
}
