package attention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type operationalEvidence struct {
	rows []deployment.OperationalObservation
}

func (o operationalEvidence) Operational(context.Context, ids.ID) ([]deployment.OperationalObservation, error) {
	return o.rows, nil
}

func TestDeploymentConditionsSeparateAttemptRuntimeAndHoldGenerations(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	d := deployment.OperationalObservation{ID: idstest.N(1), Name: "office", ObservedGeneration: 1, DesiredGeneration: 2, Readiness: deployment.ReadinessFailed, UpdatedAt: at.Format(time.RFC3339Nano), Runtime: deployment.RuntimeObservation{State: deployment.RuntimeRunning, ObservedAt: at}, Hold: &deployment.CrashLoopHold{Confirmed: true, At: at.Format(time.RFC3339Nano)}}
	read := func() Snapshot {
		t.Helper()
		got, err := (Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{d}}, Now: func() time.Time { return at }}).ProjectSnapshot(t.Context(), idstest.N(2))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read()
	if len(got.Items) != 1 || !strings.Contains(got.Items[0].Detail, "previously deployed generation is still running") || !strings.Contains(got.Items[0].Key, "/1/2/attempt") {
		t.Fatalf("failed update/old hold %+v", got)
	}
	d.Runtime = deployment.RuntimeObservation{Stale: true, AttemptedAt: at}
	got = read()
	if got.Known || len(got.Items) != 3 || strings.Contains(got.Items[0].Detail, "still running") || !got.Items[1].Stale {
		t.Fatalf("unknown erased hold or invented uptime %+v", got)
	}
	d.Readiness = deployment.Reconciled
	d.Hold = nil
	d.Runtime = deployment.RuntimeObservation{State: deployment.RuntimeStopped, ObservedAt: at}
	got = read()
	// The absence is a known, current reading; only its intent is open, and
	// the advisory says so without calling the reading unknown or old.
	if !got.Known || len(got.Items) != 1 || got.Items[0].Stale || got.Items[0].Unknown || got.Items[0].Severity != severityAdvisory || got.Items[0].StatusLabel != labelStopped || !strings.Contains(got.Items[0].Title, "office: Stopped; check whether it should be running") {
		t.Fatalf("observed stop: %+v", got)
	}
	d.RuntimeState = "stopped"
	if got = read(); !got.Known || len(got.Items) != 0 {
		t.Fatalf("intentional stop needs no repair: %+v", got)
	}
	d.RuntimeState = "running"
	d.Runtime = deployment.RuntimeObservation{State: deployment.RuntimeRunning, ObservedAt: at}
	if got = read(); !got.Known || len(got.Items) != 0 {
		t.Fatalf("recovery %+v", got)
	}
}

// Pending runtime coverage must stay unknown without warning; completed failures
// and stale readings must warn even if the last known runtime was running.
func TestDeploymentRuntimeFirstCheckAndStaleReading(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		runtime   deployment.RuntimeObservation
		wantItems int
		known     bool
	}{
		{"pending", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, Stale: true}, 0, false},
		{"failed", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, AttemptedAt: at, Err: errors.New("probe failed"), Stale: true}, 1, false},
		{"stale", deployment.RuntimeObservation{State: deployment.RuntimeRunning, ObservedAt: at, Stale: true}, 1, true},
		{"fresh", deployment.RuntimeObservation{State: deployment.RuntimeRunning, ObservedAt: at}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{{ID: idstest.N(1), ObservedGeneration: 1, Name: "office", Runtime: tc.runtime}}}, Now: func() time.Time { return at.Add(time.Hour) }}
			got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
			if err != nil || got.Known != tc.known || got.Stale != tc.runtime.Stale || len(got.Items) != tc.wantItems {
				t.Fatalf("runtime coverage: %+v %v", got, err)
			}
			if tc.wantItems > 0 && (!strings.Contains(got.Items[0].Title, "App status not known") || !got.Items[0].Stale) {
				t.Fatalf("runtime warning: %+v", got.Items)
			}
		})
	}
}

// One late or failed reading is not listed while the last good reading is
// recent; once it is older than the grace, the missing status is listed.
func TestDeploymentRuntimeMissedReadingWaitsOutTheGrace(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		runtime deployment.RuntimeObservation
		age     time.Duration
		listed  bool
	}{
		{"late, recent", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, ObservedAt: at, Stale: true}, time.Minute, false},
		{"failed, recent", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, ObservedAt: at, AttemptedAt: at.Add(time.Minute), Err: errors.New("deadline exceeded"), Stale: true}, time.Minute, false},
		{"late, past the grace", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, ObservedAt: at, Stale: true}, 3 * time.Minute, true},
		{"never read, failed", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, AttemptedAt: at, Err: errors.New("deadline exceeded"), Stale: true}, time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{{ID: idstest.N(1), ObservedGeneration: 1, Name: "office", Runtime: tc.runtime}}}, Now: func() time.Time { return at.Add(tc.age) }}
			got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
			if err != nil || (len(got.Items) > 0) != tc.listed || got.Known == tc.listed || got.Stale != tc.listed {
				t.Fatalf("grace: %+v %v", got, err)
			}
		})
	}
}

// The grace covers only the missing status: a crash hold stays listed.
func TestDeploymentGraceNeverHidesACrashHold(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	d := deployment.OperationalObservation{ID: idstest.N(1), ObservedGeneration: 1, Name: "office", Runtime: deployment.RuntimeObservation{State: deployment.RuntimeUnknown, ObservedAt: at, Stale: true}, Hold: &deployment.CrashLoopHold{Confirmed: true, At: at.Format(time.RFC3339Nano)}}
	got, err := (Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{d}}, Now: func() time.Time { return at.Add(time.Minute) }}).ProjectSnapshot(t.Context(), idstest.N(2))
	if err != nil || len(got.Items) != 1 || !strings.Contains(got.Items[0].Title, "Stopped after repeated crashes") {
		t.Fatalf("crash hold during grace: %+v %v", got, err)
	}
}

// Grace is anchored to the deployment's last act, including a first release and a rebuild;
// metadata edits cannot extend it and an unattempted observer cannot wait forever.
func TestRuntimeGraceUsesLastSuccessfulAttempt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for _, attempted := range []bool{false, true} {
		d := deployment.OperationalObservation{ID: idstest.N(1), Name: "main", LastApplied: &deployment.AppliedAttempt{At: at, Generation: 1}, LastActAt: at, Runtime: deployment.RuntimeObservation{State: deployment.RuntimeUnknown, Stale: true}}
		if attempted {
			d.Runtime.AttemptedAt = at
			d.Runtime.Err = errors.New("not read")
		}
		now := at.Add(time.Minute)
		read := func(want int) {
			t.Helper()
			got, err := (Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{d}}, Now: func() time.Time { return now }}).ProjectSnapshot(t.Context(), idstest.N(2))
			if err != nil || len(got.Items) != want {
				t.Fatalf("attempted %v: %+v %v", attempted, got, err)
			}
		}
		read(0)
		d.UpdatedAt = now.Format(time.RFC3339Nano)
		now = at.Add(2 * time.Minute)
		read(1)
		d.LastApplied, d.LastActAt = &deployment.AppliedAttempt{At: now, Generation: 2}, now
		read(0)
		now = now.Add(2 * time.Minute)
		read(1)
	}
}

// A deployment with nothing applied yet (new, or its first deploy still
// running) has no runtime to miss: it is neither stopped nor unknown.
func TestNothingAppliedRaisesNoRuntimeCondition(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for _, runtime := range []deployment.RuntimeObservation{
		{State: deployment.RuntimeStopped, ObservedAt: at, AttemptedAt: at},
		{State: deployment.RuntimeUnknown, AttemptedAt: at, Err: errors.New("deployment replacement is in progress"), Stale: true},
	} {
		d := deployment.OperationalObservation{ID: idstest.N(1), Name: "staging", DesiredGeneration: 1, Runtime: runtime}
		got, err := (Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{d}}, Now: func() time.Time { return at.Add(time.Hour) }}).ProjectSnapshot(t.Context(), idstest.N(2))
		if err != nil || !got.Known || got.Stale || len(got.Items) != 0 {
			t.Fatalf("nothing applied, runtime %+v: %+v %v", runtime, got, err)
		}
	}
}

// A runtime reading begun before the deployment's last act, or taken while a
// runtime control is under way, raises no runtime item during the grace: the
// deployment's own status says it is changing. Release and crash-hold items
// stay, and past the grace the runtime items return.
func TestRuntimeItemsWaitForAReadingBegunAfterTheAct(t *testing.T) {
	t.Parallel()
	acted := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	before, after := acted.Add(-time.Second), acted.Add(time.Second)
	for _, tc := range []struct {
		name    string
		state   string
		runtime deployment.RuntimeObservation
		hold    bool
	}{
		{"unread after a start", "running", deployment.RuntimeObservation{State: deployment.RuntimeUnknown, Stale: true}, false},
		{"stopped while stopping", "stopping", deployment.RuntimeObservation{State: deployment.RuntimeStopped, AttemptedAt: after, ObservedAt: after}, false},
		{"stopped, begun during a restart", "running", deployment.RuntimeObservation{State: deployment.RuntimeStopped, AttemptedAt: before, ObservedAt: after}, false},
		{"unhealthy, begun before a health restart", "running", deployment.RuntimeObservation{State: deployment.RuntimeRunning, Health: "unhealthy", AttemptedAt: before, ObservedAt: before}, false},
		{"stopped before the act, crash hold", "running", deployment.RuntimeObservation{State: deployment.RuntimeStopped, AttemptedAt: before, ObservedAt: after}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := deployment.OperationalObservation{ID: idstest.N(1), ObservedGeneration: 1, Name: "office", RuntimeState: tc.state, LastApplied: &deployment.AppliedAttempt{At: acted.Add(-time.Hour), Generation: 1}, LastActAt: acted, Runtime: tc.runtime}
			if tc.hold {
				d.Hold = &deployment.CrashLoopHold{Confirmed: true, At: acted.Format(time.RFC3339Nano)}
			}
			read := func(now time.Time) []Item {
				t.Helper()
				got, err := (Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{d}}, Now: func() time.Time { return now }}).ProjectSnapshot(t.Context(), idstest.N(2))
				if err != nil {
					t.Fatal(err)
				}
				return got.Items
			}
			var runtime, holds int
			for _, it := range read(acted.Add(time.Minute)) {
				if strings.HasSuffix(it.Key, "/runtime") {
					runtime++
				}
				if strings.HasSuffix(it.Key, "/crash-hold") {
					holds++
				}
			}
			if runtime != 0 || (holds == 1) != tc.hold {
				t.Fatalf("during the grace: runtime items %d, crash holds %d", runtime, holds)
			}
			if items := read(acted.Add(3 * time.Minute)); len(items) == 0 {
				t.Fatal("past the grace the runtime items did not return")
			}
		})
	}
}
