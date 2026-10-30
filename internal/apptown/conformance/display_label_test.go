package conformance

import (
	"encoding/json"
	"testing"
)

func TestCheckLabelsKeepExceptionsAndUnknownEvidenceDistinct(t *testing.T) {
	for _, tc := range []struct {
		s    ObservationState
		want CheckLabel
	}{
		{Conforms, CheckPassed}, {DoesNotConform, CheckFailed}, {EvaluationError, CheckUnknown}, {Excepted, CheckExcepted}, {NotApplicable, CheckNotApplicable}, {CannotAssess, CheckUnknown}, {Stale, CheckUnknown}, {NotScanned, CheckUnknown},
	} {
		if got := displayLabel(tc.s); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.s.String(), got, tc.want)
		}
	}
}

func TestDisplayLabelUsesCurrentInterpretationWithoutChangingSavedEvidence(t *testing.T) {
	saved := Verdict{State: Conforms, SHA: "old", Version: 1}
	original := saved
	current := Observe(saved, CurrentContext{Check: CheckRef{Version: 1}, HeadSHA: "new"})
	body, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		State string `json:"state"`
		Label string `json:"statusLabel"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.State != "stale" || wire.Label != "unknown" || saved != original {
		t.Fatalf("display changed evidence or hid uncertainty: %s", body)
	}
	body, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &wire); err != nil || wire.Label != "passed" {
		t.Fatalf("saved verdict label: %s %v", body, err)
	}
}

func TestStandingLabelsDoNotPresentIncompleteReviewsAsPassed(t *testing.T) {
	for state, want := range map[Standing]CheckLabel{StandingDoesNot: CheckFailed, StandingBroken: CheckUnknown, StandingNeedsLook: CheckUnknown, StandingSoFar: CheckUnknown, StandingExcepted: CheckExcepted, StandingConforms: CheckPassed, StandingNothing: CheckUnknown} {
		if got := state.Label(); got != want {
			t.Errorf("%s: %s, want %s", state, got, want)
		}
	}
}

func TestFindingsLabelsRequireReadableScanReports(t *testing.T) {
	for _, c := range []struct {
		actionable, reports int
		unread              bool
		want                CheckLabel
	}{{0, 0, false, CheckUnknown}, {0, 1, false, CheckPassed}, {0, 1, true, CheckUnknown}, {1, 1, false, CheckFailed}, {1, 2, true, CheckFailed}} {
		if got := FindingsLabel(c.actionable, c.reports, c.unread); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}
