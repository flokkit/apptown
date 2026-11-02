package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
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
// attempt and a safe reason. Age marks evidence stale after fifteen minutes and unknown after six hours.
func TestProtectionFailureExplainsRetainedEvidenceAndRecovery(t *testing.T) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	now := at
	dep := deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), Kind: deployment.Standing, Name: "main"}
	identity := strings.Repeat("a", 64)
	result := &Protection{}
	var readErr error
	observer := &ProtectionObserver{db: testDB(t), now: func() time.Time { return now }, resolve: func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return &dep, identity, nil }, read: func(context.Context, ids.ID, string) (*Protection, error) { return result, readErr }}
	run := func() ProtectionSnapshot {
		t.Helper()
		_ = observer.run(t.Context(), dep.ID, identity)
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
	result = &Protection{}
	result.recordDataFailure(errors.New("provider secret and private volume"))
	if err := observer.run(t.Context(), dep.ID, identity); err == nil || err.Error() != result.Unknown {
		t.Fatalf("held-data failure was reported as a successful job: %v", err)
	}
	got, _ = observer.Snapshot(t.Context(), dep.ID)
	if got.Unknown || got.Stale || got.FailedAt != now || got.ObservedAt != at || got.AttemptedAt != now || got.Reason != "The deployment's data could not be read." {
		t.Fatalf("failed held-data check hidden: %+v", got)
	}
	result = &Protection{}
	got = run()
	if got.Unknown || got.Stale || got.Reason != "" || got.ObservedAt != now {
		t.Fatalf("recovery not cleared: %+v", got)
	}
	readErr = errors.New("provider secret")
	got = run()
	if got.Unknown || got.Reason != "The protection check failed." {
		t.Fatalf("read failure hidden: %+v", got)
	}
	readErr = context.DeadlineExceeded
	got = run()
	if got.Unknown || got.Reason != "The protection check timed out." {
		t.Fatalf("timeout hidden: %+v", got)
	}
	readErr = nil
	_ = run()
	now = now.Add(30 * time.Minute)
	got, _ = observer.Snapshot(t.Context(), dep.ID)
	if got.Unknown || !got.Stale || got.Reason != "" {
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
		{"declared data", Protection{Coverage: Coverage{NotAssessed: []string{"private volume"}}}, "Protection could not be assessed for some declared data."},
		{"copy", Protection{Items: []ItemProtection{{Copy: copyUnknown}}}, "The latest data copy was interrupted."},
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

func TestProtectionReadingSurvivesRestartAndBecomesUnknownAfterSixHours(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	dep := &deployment.Deployment{ID: idstest.N(1), Kind: deployment.Standing}
	resolve := func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return dep, "identity", nil }
	observer := &ProtectionObserver{db: db, now: func() time.Time { return now }, resolve: resolve, read: func(context.Context, ids.ID, string) (*Protection, error) { return &Protection{}, nil }}
	if err := observer.run(t.Context(), dep.ID, "identity"); err != nil {
		t.Fatal(err)
	}
	restarted := &ProtectionObserver{db: db, now: func() time.Time { return now }, resolve: resolve}
	now = first.Add(30 * time.Minute)
	got, err := restarted.Snapshot(t.Context(), dep.ID)
	if err != nil || got.Unknown || !got.Stale || got.ObservedAt != first {
		t.Fatalf("restart: %+v %v", got, err)
	}
	now = first.Add(6*time.Hour + time.Second)
	got, err = restarted.Snapshot(t.Context(), dep.ID)
	if err != nil || !got.Unknown {
		t.Fatalf("expiry: %+v %v", got, err)
	}
}

func TestNativeBackupFailureKeepsKnownValuesAndTheFiveMinuteReadBudgetAcrossRestart(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	id, _ := w.clusterOf(dep)["id"].(string)
	w.api.SeedNativeBackups(id, "2026-10-01T06:10:00Z")
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	w.s.clock = func() time.Time { return now }
	md := w.s.providers.ManagedData(w.target)
	plan := backupPlanItem{Instance: id, Native: "do-database-backups"}
	good := w.s.nativeOf(t.Context(), md, plan)
	if good.Count != 1 || good.Unknown != "" {
		t.Fatalf("first read: %+v", good)
	}
	now = first.Add(30 * time.Minute)
	w.api.Fail("GET", "/v2/databases/"+id+"/backups", 500)
	failed := w.s.nativeOf(t.Context(), md, plan)
	if failed.Count != 1 || failed.Unknown != "" || !failed.CheckFailed || failed.FailedAt != now || failed.ObservedAt != first {
		t.Fatalf("failed check erased evidence: %+v", failed)
	}
	before := len(w.api.Snapshot().Requests)
	restarted := &Service{core: w.s.core, nativeFresh: nativeReadFor}
	again := restarted.nativeOf(t.Context(), md, plan)
	if len(w.api.Snapshot().Requests) != before || !again.CheckFailed || again.ObservedAt != first {
		t.Fatalf("restart lost failure read budget: %+v", again)
	}
	now = first.Add(6*time.Hour + time.Second)
	expired := restarted.nativeOf(t.Context(), md, plan)
	if expired.Unknown == "" {
		t.Fatalf("native evidence never expired: %+v", expired)
	}
	now = now.Add(nativeReadFor + time.Second)
	w.api.Fail("GET", "/v2/databases/"+id+"/backups", 0)
	recovered := restarted.nativeOf(t.Context(), md, plan)
	if recovered.Stale || recovered.Unknown != "" || recovered.CheckFailed || recovered.ObservedAt != now {
		t.Fatalf("successful refresh stayed old: %+v", recovered)
	}
}

func TestNativeReadingFreshnessIsClassifiedByOwner(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	n := &NativeBackups{ObservedAt: at}
	if nativeBackupAt(n, at.Add(time.Minute)).Stale {
		t.Fatal("fresh reading marked old")
	}
	if !nativeBackupAt(n, at.Add(20*time.Minute)).Stale || n.Unknown != "" {
		t.Fatalf("old reading: %+v", n)
	}
}

func TestProtectionFirstReadingDoesNotClaimFailure(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	got := protectionSnapshotAt(ProtectionSnapshot{}, at)
	if got.Reason != "Backups have not been checked yet." || !got.AttemptedAt.IsZero() || !got.Unknown {
		t.Fatalf("first reading: %+v", got)
	}
	failed := protectionSnapshotAt(ProtectionSnapshot{AttemptedAt: at, FailedAt: at, Reason: "The protection check timed out."}, at)
	if failed.Reason != "The protection check timed out." {
		t.Fatalf("failed reading became pending: %+v", failed)
	}
}

// Each failed data read keeps its safe cause and action across a restart,
// including when an older successful assessment remains available.
func TestProtectionDataFailureRecordsCauseAndUsefulAction(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cause          error
		reason, action string
	}{
		{"unclassified", errors.New("private volume and provider secret"), "The deployment's data could not be read.", "Open deployment settings to check the deployment."},
		{"inventory", errors.Join(errDataNotKnown, errDataInventoryUnavailable), "AppTown could not read what this deployment holds.", "The background check will try again in five minutes."},
		{"absent", errHeldDataAbsent, "The data this deployment names is not on its host.", "Open deployment settings to check the deployment."},
		{"contract", errContractUnreadable, "AppTown could not read this deployment's release contract.", "Open deployment settings to check the deployment."},
		{"Docker access", deployment.ErrNoLocalDocker, "This AppTown has no local Docker access.", "Open deployment settings to check the deployment."},
		{"project", errProjectUnreadable, "AppTown could not read this deployment's project.", "Open deployment settings to check the deployment."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			now := at
			dep := &deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), Kind: deployment.Standing}
			result := &Protection{}
			observer := &ProtectionObserver{db: testDB(t), now: func() time.Time { return now }, resolve: func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return dep, "identity", nil }, read: func(context.Context, ids.ID, string) (*Protection, error) { return result, nil }}
			if err := observer.run(t.Context(), dep.ID, "identity"); err != nil {
				t.Fatal(err)
			}
			now = at.Add(time.Minute)
			result = &Protection{}
			result.recordDataFailure(fmt.Errorf("%w: private provider credential", tc.cause))
			if err := observer.run(t.Context(), dep.ID, "identity"); err == nil {
				t.Fatal("failed reading reported success")
			}
			restarted := &ProtectionObserver{db: observer.db, now: observer.now, resolve: observer.resolve}
			got, err := restarted.Snapshot(t.Context(), dep.ID)
			if err != nil || got.Reason != tc.reason || got.NextStep != tc.action || got.AttemptedAt != now || got.ObservedAt != at {
				t.Fatalf("recorded failure: %+v %v", got, err)
			}
			result = &Protection{}
			now = now.Add(time.Minute)
			if err := observer.run(t.Context(), dep.ID, "identity"); err != nil {
				t.Fatal(err)
			}
			got, err = restarted.Snapshot(t.Context(), dep.ID)
			if err != nil || got.Reason != "" || got.NextStep != "" || got.ObservedAt != now {
				t.Fatalf("recovered check kept failure instructions: %+v %v", got, err)
			}
		})
	}
}
