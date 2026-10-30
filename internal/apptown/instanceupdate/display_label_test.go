package instanceupdate

import "testing"

func TestUpdateLabelsDoNotReportExpiredEvidenceAsAvailable(t *testing.T) {
	for _, tc := range []struct {
		state string
		label UpdateLabel
	}{{"unknown", UpdateUnknown}, {"checking", UpdateWaiting}, {"no-release", UpdateNone}, {"current", UpdateCurrent}, {"available", UpdateAvailable}, {"stale", UpdateUnknown}, {"unavailable", UpdateFailed}} {
		if got := updateLabel(tc.state); got != tc.label {
			t.Fatalf("%s: %s", tc.state, got)
		}
	}
}
