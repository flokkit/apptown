package attention

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/execution"
)

type workerEvidence struct{ value maintenance.WorkerSnapshot }

func (w workerEvidence) Snapshot(context.Context) (maintenance.WorkerSnapshot, error) {
	return w.value, nil
}
func TestWorkerAttentionRetainsUnavailableUntilObservedRecovery(t *testing.T) {
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value maintenance.WorkerSnapshot
		count int
		known bool
	}{
		{"not configured", maintenance.WorkerSnapshot{}, 0, true},
		{"cold", maintenance.WorkerSnapshot{Configured: true, Unknown: true, Stale: true}, 0, false},
		{"down", maintenance.WorkerSnapshot{Configured: true, Identity: "worker-a", Availability: execution.WorkerUnavailable, ObservedAt: at}, 1, true},
		{"unknown after down", maintenance.WorkerSnapshot{Configured: true, Identity: "worker-a", Availability: execution.WorkerUnavailable, ObservedAt: at, Unknown: true, Stale: true}, 1, false},
		{"recovered", maintenance.WorkerSnapshot{Configured: true, Identity: "worker-a", Availability: execution.WorkerAvailable, ObservedAt: at}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := (Workers{Observer: workerEvidence{tc.value}}).Snapshot(t.Context())
			if err != nil || v.Known != tc.known || len(v.Items) != tc.count {
				t.Fatal(v, err)
			}
			if tc.count > 0 && (v.Items[0].Key != "worker-a" || v.Items[0].ObservedAt != at || v.Items[0].Stale != tc.value.Stale || v.Items[0].Href != "/infrastructure") {
				t.Fatal(v)
			}
		})
	}
}
