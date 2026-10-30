package integration

import (
	"testing"
	"time"
)

func TestHealthLabelsKeepUncheckedDifferentFromSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		h    Health
		want HealthStatus
	}{
		{Health{}, HealthUnknown}, {Health{OK: true}, HealthUnknown}, {Health{Checked: true, OK: true}, HealthPassed}, {Health{Checked: true}, HealthFailed},
	} {
		if got := tc.h.ForDisplay(); got.StateLabel != tc.want {
			t.Fatalf("%+v became %s", tc.h, got.StateLabel)
		}
	}
	if got := (Health{OK: true}).At(time.Now()); got.StateLabel != HealthPassed {
		t.Fatalf("checked reading: %+v", got)
	}
}
