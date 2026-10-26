package attention

import (
	"context"
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
	d.Runtime = deployment.RuntimeObservation{Stale: true}
	got = read()
	if got.Known || len(got.Items) != 3 || strings.Contains(got.Items[0].Detail, "still running") || !got.Items[1].Stale {
		t.Fatalf("unknown erased hold or invented uptime %+v", got)
	}
	d.Readiness = deployment.Reconciled
	d.Hold = nil
	d.Runtime = deployment.RuntimeObservation{State: deployment.RuntimeStopped, ObservedAt: at}
	got = read()
	if got.Known || len(got.Items) != 1 || got.Items[0].Severity != SeverityAdvisory || !strings.Contains(got.Items[0].Title, "intent is unknown") {
		t.Fatalf("invented unintentional stop %+v", got)
	}
	d.Runtime = deployment.RuntimeObservation{State: deployment.RuntimeRunning, ObservedAt: at}
	if got = read(); !got.Known || len(got.Items) != 0 {
		t.Fatalf("recovery %+v", got)
	}
}
