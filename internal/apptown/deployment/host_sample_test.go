package deployment

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// An up host must return its sample too: reachability and inventory share the
// same evidence even when a background lap publishes a failure during the read.
func TestHostSampleSharedByReachabilityAndDockerInventory(t *testing.T) {
	t.Parallel()
	s, db, daemon, projectID, target := healthWorld(t)
	for _, name := range []string{"unreleased", "standing"} {
		if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, name, target.ID, Attributes{}.WithDefaults()); err != nil {
			t.Fatal(err)
		}
	}
	snapshots := NewHostSnapshots(db, s.now)
	lap := func() {
		t.Helper()
		if _, _, err := snapshots.collect(t.Context(), target, docker.Scope{Client: daemon, Now: s.now}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, nil, s.now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	lap()
	placements, err := s.Deployments.OnTargets(t.Context(), []ids.ID{target.ID})
	if err != nil {
		t.Fatal(err)
	}
	count, stop := db.CountStatements("-- name: TargetReadings :many")
	ctx, err := s.WithHostSamples(t.Context(), []*Target{target}, placements)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.HostReachability(ctx, target.ID)
	if err != nil || host.Down {
		t.Fatalf("up host: %+v %v", host, err)
	}
	inventory, err := s.InventoryFor(ctx, target)
	if err != nil || inventory.Unknown {
		t.Fatalf("up inventory: %+v %v", inventory, err)
	}
	stop()
	if count() != 1 {
		t.Fatalf("host sample read %d times", count())
	}
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "info" {
			return nil, errors.New("connection refused"), true
		}
		return nil, nil, false
	})
	lap()
	lap()
	sameHost, err := s.HostReachability(ctx, target.ID)
	if err != nil || !reflect.DeepEqual(host, sameHost) {
		t.Fatalf("request changed host sample: %+v %v", sameHost, err)
	}
	sameInventory, err := s.InventoryFor(ctx, target)
	if err != nil || !reflect.DeepEqual(inventory, sameInventory) {
		t.Fatalf("request changed inventory sample: %+v %v", sameInventory, err)
	}
	fresh, err := s.WithHostSamples(t.Context(), []*Target{target}, placements)
	if err != nil {
		t.Fatal(err)
	}
	down, err := s.HostReachability(fresh, target.ID)
	if err != nil || !down.Down || down.Deployments != 2 {
		t.Fatalf("next request: %+v %v", down, err)
	}
	done, err := snapshots.BeforeAct(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	done()
	fresh, err = s.WithHostSamples(t.Context(), []*Target{target}, placements)
	if err != nil {
		t.Fatal(err)
	}
	invalidated, err := s.InventoryFor(fresh, target)
	if err != nil || !invalidated.Invalidated {
		t.Fatalf("act fence lost: %+v %v", invalidated, err)
	}
	target.SettingsRevision++
	fresh, err = s.WithHostSamples(t.Context(), []*Target{target}, placements)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.InventoryFor(fresh, target)
	if err != nil || !changed.Unknown {
		t.Fatalf("revision fence lost: %+v %v", changed, err)
	}
}
