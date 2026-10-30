package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/deploy"
	"testing"
	"time"
)

// Reachability is a provider fact, never inferred from an error's wording.
func TestRuntimeReachabilityRetainsFirstFailureUntilSuccessOrPlacementChange(t *testing.T) {
	var failure error
	s, _, envs, project := listingFixture(t, 1, func(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
		return deploy.Observation{Generation: p.Generation, Rollout: "active"}, failure
	})
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	read := func() RuntimeObservation {
		t.Helper()
		if err := s.ObserveRuntime(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, err := s.List(t.Context(), project)
		if err != nil {
			t.Fatal(err)
		}
		return rows[0].Observation
	}
	if got := read(); got.State != RuntimeRunning {
		t.Fatalf("running: %+v", got)
	}
	failure = deploy.Unreachable(errors.New("connection refused"))
	now = now.Add(time.Second)
	first := read()
	if first.State != RuntimeUnreachable || !first.UnreachableSince.Equal(now) || first.TargetName == "" || first.State.Running() != nil {
		t.Fatalf("first failure: %+v", first)
	}
	now = now.Add(time.Minute)
	if got := read(); got.State != RuntimeUnreachable || got.UnreachableSince != first.UnreachableSince {
		t.Fatalf("repeated failure: %+v", got)
	}
	if _, err := s.EditTargetLabel(t.Context(), envs[0].TargetID, first.TargetName, "Club host"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if got := read(); got.UnreachableSince != first.UnreachableSince || got.TargetName != "Club host" {
		t.Fatalf("label edit reset reachability: %+v", got)
	}
	failure = errors.New("invalid provider response")
	if got := read(); got.State != RuntimeUnknown || got.UnreachableSince != first.UnreachableSince {
		t.Fatalf("unknown failure broke the failure run: %+v", got)
	}
	failure = deploy.Unreachable(errors.New("connection refused"))
	if got := read(); got.UnreachableSince != first.UnreachableSince {
		t.Fatalf("resumed unreachable restarted the clock: %+v", got)
	}
	failure = nil
	if got := read(); got.State != RuntimeRunning || !got.UnreachableSince.IsZero() {
		t.Fatalf("recovery: %+v", got)
	}
	now = now.Add(time.Second)
	failure = deploy.Unreachable(errors.New("timeout"))
	if got := read(); !got.UnreachableSince.Equal(now) {
		t.Fatalf("new failure: %+v", got)
	}
	// A new desired generation is a new placement identity. No provider is asked.
	e := envs[0]
	e.DesiredGeneration++
	records, err := s.listRecords(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	records[0].deployment = e
	if got := s.observations.read(records[0], now); got.State != RuntimeUnknown || !got.UnreachableSince.IsZero() {
		t.Fatalf("replacement: %+v", got)
	}
	failure = errors.New("host unreachable")
	if got := read(); got.State != RuntimeUnknown {
		t.Fatalf("untyped failure classified from wording: %+v", got)
	}
}

func TestRuntimeIdentityIgnoresPresentationAndStatusButBindsPlacement(t *testing.T) {
	r := listingRecord{deployment: &Deployment{DesiredGeneration: 1}, target: &Target{DisplayLabel: "Host", Kind: KindDOApp, Status: TargetReady, Address: map[string]any{"region": "syd"}}}
	first := reachabilityKey(r)
	r.target.DisplayLabel = "Club host"
	r.target.Status = TargetUnreachable
	r.target.SettingsRevision++
	if reachabilityKey(r) != first {
		t.Fatal("presentation or check status reset the failure streak")
	}
	r.target.Address = map[string]any{"region": "nyc"}
	if reachabilityKey(r) == first {
		t.Fatal("connection identity changed without resetting runtime")
	}
	r.target.Address = map[string]any{"region": "syd"}
	r.deployment.DesiredGeneration++
	if reachabilityKey(r) == first {
		t.Fatal("replacement inherited the failure streak")
	}
}

func TestExpiredSweepLeavesRuntimeUnknown(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	s := &Service{}
	v := s.observed(ctx, RuntimeObservation{State: RuntimeUnknown}, listingRecord{}, deploy.Observation{}, deploy.Unreachable(errors.New("dial timed out")))
	if v.State != RuntimeUnknown || !v.UnreachableSince.IsZero() || !errors.Is(v.Err, context.DeadlineExceeded) {
		t.Fatalf("sweep budget claimed host unreachability: %+v", v)
	}
}
