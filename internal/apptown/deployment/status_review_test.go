package deployment

import (
	"testing"
	"time"
)

func TestReviewSettledUncheckedReleaseDoesNotSayDeploying(t *testing.T) {
	at := time.Date(2026, 11, 7, 6, 32, 39, 0, time.UTC)
	dep := Deployment{ObservedGeneration: 16, LastApplied: &AppliedAttempt{At: at, Generation: 16}}
	a := deploymentAnswer(&dep, RuntimeObservation{State: RuntimeUnknown, Stale: true}, Diagnostics{}, nil, &Release{Number: 18, Generation: 16})
	if a.Reason != "release 18 live — first check pending" {
		t.Fatalf("missing first-check answer: %+v", a)
	}
	if a.Word == progressLabelDeploying {
		t.Fatalf("settled release still says deploying beside live: %+v", a)
	}
}
