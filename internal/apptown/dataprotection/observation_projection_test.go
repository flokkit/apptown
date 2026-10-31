package dataprotection

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"strings"
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

// A failed assessment retains the evidence it could not renew, with the latest
// attempt and a safe reason. Age alone expires evidence only after 12 minutes.
func TestProtectionFailureExplainsRetainedEvidenceAndRecovery(t *testing.T) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	now := at
	dep := deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), Kind: deployment.Standing, Name: "main"}
	identity := strings.Repeat("a", 64)
	result := &Protection{}
	var readErr error
	observer := &ProtectionObserver{now: func() time.Time { return now }, resolve: func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return &dep, identity, nil }, read: func(context.Context, ids.ID, string) (*Protection, error) { return result, readErr }}
	run := func() ProtectionSnapshot {
		t.Helper()
		_ = observer.Run(t.Context(), dep.ID, identity)
		got, err := observer.Snapshot(t.Context(), dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := run(); got.Unknown || got.Stale || got.Reason != "" {
		t.Fatalf("successful empty check: %+v", got)
	}
	now = at.Add(9 * time.Minute)
	got, _ := observer.Snapshot(t.Context(), dep.ID)
	if got.Unknown || got.Stale {
		t.Fatalf("nine minutes is not overdue: %+v", got)
	}
	result = &Protection{Unknown: "provider secret and private volume"}
	if err := observer.Run(t.Context(), dep.ID, identity); err == nil || err.Error() != result.Unknown {
		t.Fatalf("held-data failure was reported as a successful job: %v", err)
	}
	got, _ = observer.Snapshot(t.Context(), dep.ID)
	if !got.Unknown || !got.Stale || got.ObservedAt != at || got.AttemptedAt != now || got.Reason != "The deployment's data could not be read." {
		t.Fatalf("failed held-data check hidden: %+v", got)
	}
	result = &Protection{}
	got = run()
	if got.Unknown || got.Stale || got.Reason != "" || got.ObservedAt != now {
		t.Fatalf("recovery not cleared: %+v", got)
	}
	readErr = errors.New("provider secret")
	got = run()
	if !got.Unknown || got.Reason != "The protection check failed." {
		t.Fatalf("read failure hidden: %+v", got)
	}
	readErr = context.DeadlineExceeded
	got = run()
	if !got.Unknown || got.Reason != "The protection check timed out." {
		t.Fatalf("timeout hidden: %+v", got)
	}
	readErr = nil
	_ = run()
	now = now.Add(13 * time.Minute)
	got, _ = observer.Snapshot(t.Context(), dep.ID)
	if !got.Unknown || !got.Stale || got.Reason != "The protection check is overdue." {
		t.Fatalf("overdue evidence claimed known: %+v", got)
	}
}

func TestProtectionReasonsNameTheFailedCheckWithoutProviderDetails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		protection Protection
		reason     string
	}{
		{"held data", Protection{Unknown: "private volume and provider secret"}, "The deployment's data could not be read."},
		{"declared data", Protection{Coverage: Coverage{NotAssessed: []string{"private volume"}}}, "Protection could not be assessed for some declared data."},
		{"copy", Protection{Items: []ItemProtection{{Copy: CopyUnknown}}}, "The latest data copy was interrupted."},
		{"native", Protection{Items: []ItemProtection{{Native: &NativeBackups{Unknown: "provider secret"}}}}, "The provider backup check failed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := observedProtection(&tc.protection, time.Now())
			if !got.Unknown || got.Reason != tc.reason {
				t.Fatalf("failure reason lost: %+v", got)
			}
		})
	}
}
