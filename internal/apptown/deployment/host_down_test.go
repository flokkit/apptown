package deployment

import (
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// The loop confirms an outage after two consecutive host failures, retains its
// first failure time across restarts and partial recovery, and clears on a full lap.
func TestHostDownRequiresTwoLapsAndCompleteRecovery(t *testing.T) {
	t.Parallel()
	svc, db, daemon, projectID, target := healthWorld(t)
	for _, name := range []string{"main", "office"} {
		if _, err := svc.Deployments.EnsureStanding(t.Context(), projectID, name, target.ID, Attributes{}.WithDefaults()); err != nil {
			t.Fatal(err)
		}
	}
	first := time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)
	now := first
	svc.setClock(func() time.Time { return now })
	failInfo, failContainers := false, false
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if failInfo && args[0] == "info" {
			return nil, errors.New("connection refused"), true
		}
		if failContainers && args[0] == "ps" {
			return nil, errors.New("containers unavailable"), true
		}
		return nil, nil, false
	})
	lap := func() {
		t.Helper()
		// Recreate the owner each lap: the outage streak belongs to stored evidence.
		s := NewHostSnapshots(db, svc.now)
		if err := s.collect(t.Context(), target, docker.Scope{Client: daemon, Now: svc.now}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	check := func(down bool) HostReachability {
		t.Helper()
		got, err := svc.HostReachability(t.Context(), target.ID)
		if err != nil || got.Down != down {
			t.Fatalf("host down = %+v, %v; want %v", got, err, down)
		}
		return got
	}
	lap()
	failInfo = true
	lap()
	check(false)
	// A successful host check breaks the streak, even if another section fails.
	failInfo, failContainers = false, true
	lap()
	failInfo, failContainers = true, false
	now = first.Add(time.Minute)
	lap()
	check(false)
	now = now.Add(time.Minute)
	lap()
	got := check(true)
	if !got.Since.Equal(first.Add(time.Minute)) || got.Error != "connection refused" || got.Deployments != 2 {
		t.Fatalf("outage evidence: %+v", got)
	}
	failInfo, failContainers = false, true
	lap()
	check(true)
	failContainers = false
	lap()
	check(false)
}

// Reachability comes from stored host evidence, rather than the target kind.
func TestHostReachabilityUsesStoredReading(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, _ := healthWorld(t)
	target, err := svc.Targets.Put(t.Context(), &Target{Name: "stored-host", Kind: KindPortainer, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.HostReachability(t.Context(), target.ID)
	if err != nil || got != (HostReachability{}) {
		t.Fatalf("no host reading: %+v, %v", got, err)
	}
	// Seed through the collector to exercise the stored-reading contract
	// independently of the reader manager's target selection.
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "info" {
			return nil, errors.New("connection refused"), true
		}
		return nil, nil, false
	})
	for range 2 {
		if err := NewHostSnapshots(db, svc.now).collect(t.Context(), target, docker.Scope{Client: daemon, Now: svc.now}, docker.HostQuery{Host: true}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err = svc.HostReachability(t.Context(), target.ID)
	if err != nil || !got.Down || got.Error != "connection refused" || got.Since.IsZero() {
		t.Fatalf("stored outage: %+v, %v", got, err)
	}
}
