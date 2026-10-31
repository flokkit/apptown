package deployment

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTargetStateChecksMoveBetweenReadyAndUnreachable(t *testing.T) {
	t.Parallel()
	for _, from := range []TargetState{TargetReady, TargetUnreachable} {
		if got, err := from.Checked(true); err != nil || got != TargetReady {
			t.Fatalf("%s answered: %s %v", from, got, err)
		}
		if got, err := from.Checked(false); err != nil || got != TargetUnreachable {
			t.Fatalf("%s silent: %s %v", from, got, err)
		}
		if got, err := from.Retire(); err != nil || got != TargetRetired {
			t.Fatalf("%s retire: %s %v", from, got, err)
		}
	}
}

func TestRetiredTargetIsTerminal(t *testing.T) {
	t.Parallel()
	if _, err := TargetRetired.Checked(true); !errors.Is(err, ErrTargetRetired) {
		t.Fatalf("a check never revives a retired target: %v", err)
	}
	if _, err := TargetRetired.Retire(); !errors.Is(err, ErrTargetRetired) {
		t.Fatalf("retiring twice is refused as already done: %v", err)
	}
	if TargetRetired.Available() == nil || TargetRetired.Offerable() {
		t.Fatal("a retired target is neither available nor offerable")
	}
	if TargetUnreachable.Offerable() || !TargetReady.Offerable() {
		t.Fatal("only a target that answered may take new work")
	}
}

func TestTargetStateZeroValueIsInvalidEverywhere(t *testing.T) {
	t.Parallel()
	var zero TargetState
	if _, err := zero.Value(); err == nil {
		t.Fatal("the zero value cannot be stored")
	}
	if err := zero.Available(); !errors.Is(err, ErrTargetState) {
		t.Fatalf("the zero value is not available: %v", err)
	}
	if _, err := zero.MarshalJSON(); err == nil {
		t.Fatal("the zero value cannot be published")
	}
	var s TargetState
	if err := s.Scan("retired"); err != nil || s != TargetRetired {
		t.Fatalf("scan: %s %v", s, err)
	}
	if err := s.Scan("gone"); !errors.Is(err, ErrTargetState) {
		t.Fatalf("an unknown stored state is an error, never accepted: %v", err)
	}
	body, err := json.Marshal(struct{ Status TargetState }{TargetUnreachable})
	if err != nil || string(body) != `{"Status":"unreachable"}` {
		t.Fatalf("json: %s %v", body, err)
	}
	var back struct{ Status TargetState }
	if err := json.Unmarshal(body, &back); err != nil || back.Status != TargetUnreachable {
		t.Fatalf("json round trip: %s %v", back.Status, err)
	}
}

func TestTargetCheckLabelsAreSeparateFromAppRuntime(t *testing.T) {
	if got := TargetReady.Display().String(); got != "working" {
		t.Fatalf("host state is not plain: %s", got)
	}
	for state, want := range map[TargetState]TargetLabel{TargetReady: TargetWorking, TargetUnreachable: TargetCheckFailed, TargetRetired: TargetCheckStopped, 0: TargetCheckUnknown} {
		if got := state.Display(); got != want {
			t.Errorf("%s: %s, want %s", state, got, want)
		}
	}
}
