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
	if got.Known || len(got.Items) != 1 || got.Items[0].Severity != severityAdvisory || !strings.Contains(got.Items[0].Title, "office: Stopped; check whether it should be running") {
		t.Fatalf("invented unintentional stop %+v", got)
	}
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
			source := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{{ID: idstest.N(1), Name: "office", Runtime: tc.runtime}}}, Now: func() time.Time { return at.Add(time.Hour) }}
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
