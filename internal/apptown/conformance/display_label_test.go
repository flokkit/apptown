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
		{Conforms, checkPassed}, {DoesNotConform, checkFailed}, {EvaluationError, checkUnknown}, {Excepted, checkExcepted}, {NotApplicable, checkNotApplicable}, {CannotAssess, checkUnknown}, {Stale, checkUnknown}, {NotScanned, checkUnknown},
	} {
		if got := displayLabel(tc.s); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.s.String(), got, tc.want)
		}
	}
}

func TestDisplayLabelUsesCurrentInterpretationWithoutChangingSavedEvidence(t *testing.T) {
	saved := Verdict{State: Conforms, SHA: "old", Version: 1}
	original := saved
	current := observe(saved, currentContext{Check: CheckRef{Version: 1}, HeadSHA: "new"}, nil)
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
	body, err = json.Marshal(Recorded(saved, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &wire); err != nil || wire.Label != "passed" {
		t.Fatalf("saved verdict label: %s %v", body, err)
	}
}

func TestStandingLabelsDoNotPresentIncompleteReviewsAsPassed(t *testing.T) {
	for state, want := range map[Standing]CheckLabel{standingDoesNot: checkFailed, standingBroken: checkUnknown, StandingNeedsLook: checkUnknown, standingSoFar: checkUnknown, standingExcepted: checkExcepted, StandingConforms: checkPassed, standingNothing: checkUnknown} {
		if got := state.label(); got != want {
			t.Errorf("%s: %s, want %s", state, got, want)
		}
	}
}

func TestFindingsLabelsRequireReadableScanReports(t *testing.T) {
	for _, c := range []struct {
		actionable, reports int
		unread              bool
		want                CheckLabel
	}{{0, 0, false, checkUnknown}, {0, 1, false, checkPassed}, {0, 1, true, checkUnknown}, {1, 1, false, checkFailed}, {1, 2, true, checkFailed}} {
		if got := FindingsLabel(c.actionable, c.reports, c.unread, false); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}

func TestOldSecurityScanDoesNotClaimCurrentMainPassed(t *testing.T) {
	if got := FindingsLabel(0, 3, false, true); got != checkUnknown {
		t.Fatalf("stale clean reports: %s", got)
	}
	if got := FindingsLabel(2, 3, false, true); got != checkFailed {
		t.Fatalf("stale known problems: %s", got)
	}
}
