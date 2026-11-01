package instanceupdate

import "testing"

func TestUpdateLabelsDoNotReportExpiredEvidenceAsAvailable(t *testing.T) {
	for _, tc := range []struct {
		state string
		label UpdateLabel
	}{{"unknown", updateUnknown}, {"checking", updateWaiting}, {"no-release", updateNone}, {"current", updateCurrent}, {"available", updateAvailable}, {"stale", updateUnknown}, {"unavailable", updateFailed}} {
		if got := updateLabel(tc.state); got != tc.label {
			t.Fatalf("%s: %s", tc.state, got)
		}
	}
}
