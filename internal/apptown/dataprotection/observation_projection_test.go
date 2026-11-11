package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Targets without data access hold no deployment data. Their completed
// assessment supplies no unknown coverage or copy items to Attention.
func TestNoDataAccessTargetRecordsNothingDeclared(t *testing.T) {
	t.Parallel()
	s, _, _, _, pid := backupWorld(t, "static-site", answeringPort(t))
	target, err := s.Targets.Put(t.Context(), &deployment.Target{
		Name: "website", Kind: deployment.KindS3Bucket, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "website", target.ID, deployment.Attributes{DataClass: "production"}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	runRelease(t, s, dep, "static-release", "app:\n  web: {static: site, public: true}\n")
	o := NewProtectionObserver(s.Service, nil)
	_, identity, err := o.resolve(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.run(t.Context(), dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	reading, err := o.Snapshot(t.Context(), dep.ID)
	if err != nil || reading.Unknown || reading.Waiting || reading.Unassessed || reading.Reason != "" || len(reading.Items) != 0 || reading.ObservedAt.IsZero() {
		t.Fatalf("empty deployment raised Attention evidence: %+v %v", reading, err)
	}
	p, err := s.DeploymentProtection(t.Context(), pid, "website")
	if err != nil {
		t.Fatal(err)
	}
	if p.Note != "" || p.Unknown != "" || len(p.Items) != 0 || len(p.Coverage.Assessed) != 0 || !slices.Equal(p.Coverage.NotAssessed, []string{outsideNotAssessed}) {
		t.Fatalf("empty deployment claimed data or copies: %+v", p)
	}
}

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
// attempt and a safe reason. Age marks evidence stale after seventy-five minutes and unknown after six hours.
func TestProtectionFailureExplainsRetainedEvidenceAndRecovery(t *testing.T) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	now := at
	dep := deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), LastApplied: &deployment.AppliedAttempt{At: time.Now(), Generation: 1}, Kind: deployment.Standing, Name: "main"}
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
	now = now.Add(76 * time.Minute)
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
	dep := &deployment.Deployment{ID: idstest.N(1), Kind: deployment.Standing, LastApplied: &deployment.AppliedAttempt{At: now, Generation: 1}}
	resolve := func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return dep, "identity", nil }
	observer := &ProtectionObserver{db: db, now: func() time.Time { return now }, resolve: resolve, read: func(context.Context, ids.ID, string) (*Protection, error) { return &Protection{}, nil }}
	if err := observer.run(t.Context(), dep.ID, "identity"); err != nil {
		t.Fatal(err)
	}
	restarted := &ProtectionObserver{db: db, now: func() time.Time { return now }, resolve: resolve}
	now = first.Add(50 * time.Minute)
	got, err := restarted.Snapshot(t.Context(), dep.ID)
	if err != nil || got.Unknown || got.Stale || got.ObservedAt != first {
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
		{"inventory", errors.Join(errDataNotKnown, errDataInventoryUnavailable), "AppTown could not read what this deployment holds.", "The background check will try again within an hour."},
		{"absent", errHeldDataAbsent, "The data this deployment names is not on its host.", "Open deployment settings to check the deployment."},
		{"contract", errContractUnreadable, "AppTown could not read this deployment's release contract.", "Open deployment settings to check the deployment."},
		{"Docker access", deployment.ErrNoLocalDocker, "This AppTown has no local Docker access.", "Open deployment settings to check the deployment."},
		{"project", errProjectUnreadable, "AppTown could not read this deployment's project.", "Open deployment settings to check the deployment."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			now := at
			dep := &deployment.Deployment{ID: idstest.N(1), ProjectID: idstest.N(2), LastApplied: &deployment.AppliedAttempt{At: at, Generation: 1}, Kind: deployment.Standing}
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

func TestNewDeploymentWaitsButRestoreAdoptionStillAssessesProtection(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	ctx := t.Context()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	o := NewProtectionObserver(w.s.Service, nil)
	_, identity, err := o.resolve(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.run(ctx, dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	pending, err := o.Snapshot(ctx, dep.ID)
	if err != nil || !pending.Waiting || !pending.AttemptedAt.IsZero() || pending.Reason != "Not released yet." {
		t.Fatalf("unreleased: %+v %v", pending, err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	dep, err = w.s.deployments.ByID(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dep.LastApplied == nil {
		t.Fatal("successful attempt missing")
	}
	if !w.api.AdvanceDeployment(dep.Handle["appId"].(string), "another-release") {
		t.Fatal("provider app missing")
	}
	if _, err := w.s.deployments.HoldAfterRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (deployment.JobWorker{Service: w.s.deploymentOwner}).CheckAfterRestore(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	dep, err = w.s.deployments.ByID(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	// What AppTown applied is no longer what runs; the adopted release serves.
	if dep.ReleaseSHA != "" || dep.LastApplied != nil || !dep.ServingAttemptID.IsZero() || dep.ServingReleaseID.IsZero() || dep.RestoreCheck != "" {
		t.Fatalf("adoption did not record its observed release as serving: %+v", dep)
	}
	_, identity, err = o.resolve(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = o.run(ctx, dep.ID, identity)
	got, err := o.Snapshot(ctx, dep.ID)
	if err != nil || got.Waiting || got.AttemptedAt.IsZero() {
		t.Fatalf("restore-adopted assessment suppressed: %+v %v", got, err)
	}
}

func TestEmptyRevisionWithSuccessfulAttemptStillReportsContractFailure(t *testing.T) {
	t.Parallel()
	s, db, _, _, pid := backupWorld(t, "unknown-contract", answeringPort(t))
	dep, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	// Docker has no restore-adoption operation. Reproduce the empty revision
	// that adoption records, leaving the successful attempt intact.
	// sql-guarantee: reproduce adoption’s empty revision; no Docker operation performs restore adoption.
	mustExec(t, db, `UPDATE deployments SET release_sha='' WHERE id=$1`, dep.ID)
	collectProtectionForTest(t, s, pid, "main")
	got, err := NewProtectionObserver(s.Service, nil).Snapshot(t.Context(), dep.ID)
	if err != nil || got.Waiting || !got.Unknown || got.AttemptedAt.IsZero() || got.Reason != "AppTown could not read this deployment's release contract." {
		t.Fatalf("applied without revision: %+v %v", got, err)
	}
}

func TestChangedProtectionKeepsDisplayWithoutReusingEvidence(t *testing.T) {
	t.Parallel()
	at := time.Now()
	dep := &deployment.Deployment{ID: idstest.N(1), Kind: deployment.Standing, LastApplied: &deployment.AppliedAttempt{At: at, Generation: 1}}
	identity := "first"
	observer := &ProtectionObserver{db: testDB(t), now: func() time.Time { return at }, resolve: func(context.Context, ids.ID) (*deployment.Deployment, string, error) { return dep, identity, nil }, read: func(context.Context, ids.ID, string) (*Protection, error) {
		return &Protection{Items: []ItemProtection{{Need: "postgres", Copy: CopyFresh, CopyLabel: CopyFresh.label()}}, Operations: &DataOperations{}}, nil
	}}
	if err := observer.run(t.Context(), dep.ID, identity); err != nil {
		t.Fatal(err)
	}
	identity = "second"
	got, err := observer.Snapshot(t.Context(), dep.ID)
	if err != nil || !got.Waiting || !got.Unknown || !got.Stale || got.Protection == nil || len(got.Protection.Items) != 1 || len(got.Items) != 0 || got.Identity != "first" || !got.ObservedAt.Equal(at) {
		t.Fatalf("old display became evidence or was lost: %+v %v", got, err)
	}
	observer.read = func(context.Context, ids.ID, string) (*Protection, error) { return nil, errors.New("failed") }
	_ = observer.run(t.Context(), dep.ID, identity)
	got, err = observer.Snapshot(t.Context(), dep.ID)
	if err != nil || got.Waiting || !got.Unknown || len(got.Items) != 0 || got.Identity != "first" || got.Protection == nil {
		t.Fatalf("failed new check borrowed evidence: %+v %v", got, err)
	}
}

func TestOldProtectionPayloadCannotOfferOperationsForTheNewGeneration(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.auth.via = "session"
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := w.release("prod"); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	collectProtectionForTest(t, w.s, w.project, "prod")
	old, err := w.s.DeploymentProtection(t.Context(), w.project, "prod")
	if err != nil || old.Operations == nil {
		t.Fatalf("initial protection: %+v %v", old, err)
	}
	if rel, _ := w.releaseAs(deployment.DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: w.bundle.ID, AcceptUncopiedData: true}); rel.State != deployment.Released {
		t.Fatal(rel.State)
	}
	got, err := w.s.DeploymentProtection(t.Context(), w.project, "prod")
	if err != nil || !got.Reading.Waiting || !got.Reading.Unknown || got.Operations != nil || got.TakeDown != "" || len(got.Items) != len(old.Items) || got.Reading.Identity != old.Reading.Identity || !got.Reading.ObservedAt.Equal(old.Reading.ObservedAt) {
		t.Fatalf("old payload authorized operations or was discarded: %+v %v", got, err)
	}
	snapshot, err := NewProtectionObserver(w.s.Service, nil).Snapshot(t.Context(), dep.ID)
	if err != nil || len(snapshot.Items) != 0 {
		t.Fatalf("old items became Attention evidence: %+v %v", snapshot, err)
	}
}

func TestHourlyProtectionReadingStaysCurrentWithSlack(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	reading := ProtectionSnapshot{ObservedAt: at}
	for _, age := range []time.Duration{50 * time.Minute, 65 * time.Minute, 76 * time.Minute, 6*time.Hour + time.Second} {
		got := protectionSnapshotAt(reading, at.Add(age))
		if got.Stale != (age > 75*time.Minute) || got.Unknown != (age > 6*time.Hour) {
			t.Fatalf("age %s: %+v", age, got)
		}
	}
}

// Detail and protection coverage read the same provider timestamp.
func TestProviderBackupFreshnessAgreesWithProtectionCoverage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{15 * time.Minute, 15*time.Minute + time.Second, 50 * time.Minute, 76 * time.Minute, 6*time.Hour + time.Second} {
		now := at.Add(age)
		native := nativeBackupAt(&NativeBackups{ObservedAt: at}, now)
		snapshot := protectionSnapshotAt(ProtectionSnapshot{ObservedAt: now, Items: []ProtectionItemObservation{{Native: true, NativeAt: at}}}, now)
		if native.Stale != snapshot.Stale || (native.Unknown != "") != snapshot.Unknown {
			t.Fatalf("age %s: native %+v / coverage %+v", age, native, snapshot)
		}
	}
}
