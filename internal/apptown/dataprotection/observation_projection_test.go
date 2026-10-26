package dataprotection

import (
	"testing"
	"time"
)

func TestProtectionObservationKeepsUnknownRehearsalDistinctFromFailure(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{RehearsalFailed, RehearsalUnknown, RehearsalPassed, RehearsalPassedNoData} {
		got := observedProtection(&Protection{Rehearsal: &RehearsalRef{Outcome: outcome, Passed: outcome == RehearsalPassed || outcome == RehearsalPassedNoData}}, time.Now())
		if !got.Rehearsed || got.RehearsalOutcome != outcome {
			t.Fatalf("owner outcome lost: %+v", got)
		}
	}
}
