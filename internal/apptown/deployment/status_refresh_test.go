package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

// The review's configured-deployment reproduction must succeed even without
// an attempt to repair. The reading can be unknown, but it was requested now.
func TestStatusRefreshOfConfiguredDeployment(t *testing.T) {
	s, _, envs, project := listingFixture(t, 1, absentObservation)
	result, err := s.CheckAgain(t.Context(), project, envs[0].Name)
	if err != nil || len(result.Checked) != 0 || result.Observation.AttemptedAt.IsZero() {
		t.Fatalf("configured refresh: %+v %v", result, err)
	}
}

func TestStatusRefreshReadsSettledRuntimeAndPublishesIt(t *testing.T) {
	w := newDiagnosticsWorld(t)
	ctx := t.Context()
	if err := w.s.ObserveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	old := w.s.ObserveContainer(ctx, w.main)
	if old.State != ContainerRunning {
		t.Fatalf("initial runtime: %+v", old)
	}
	w.daemon.SetRunning(w.main.Container, false)
	now := w.advance(time.Minute)
	result, err := w.s.CheckAgain(ctx, w.project, "main")
	if err != nil || len(result.Checked) != 0 || result.Observation.State != RuntimeStopped || !result.Observation.ObservedAt.Equal(now) || result.Observation.Stale {
		t.Fatalf("settled refresh: %+v %v", result, err)
	}
	got := w.s.ObserveContainer(ctx, w.main)
	if got.State != ContainerStopped || !got.ObservedAt.Equal(now) || got.Stale {
		t.Fatalf("published runtime: %+v", got)
	}
	if d := w.reread(t); d.Readiness != Reconciled || d.ObservedGeneration != w.main.ObservedGeneration {
		t.Fatalf("refresh changed settled generation: %+v", d)
	}
}

func TestStatusRefreshOnlySamplesSelectedDeployment(t *testing.T) {
	calls := 0
	s, _, envs, project := listingFixture(t, 2, func(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
		calls++
		if p.Name != "app-0" {
			t.Errorf("sampled another deployment: %s", p.Name)
		}
		return absentObservation(context.Background(), p)
	})
	if _, err := s.CheckAgain(t.Context(), project, envs[0].Name); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("runtime reads: %d", calls)
	}
	records, err := s.listRecords(t.Context(), []ids.ID{project})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		got := s.observations.read(r, s.now())
		if (r.deployment.ID == envs[0].ID) != !got.AttemptedAt.IsZero() {
			t.Fatalf("wrong deployment refreshed: %s %+v", r.deployment.Name, got)
		}
	}
}
