package attention

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
)

type waitingReleases []deployment.WaitingRelease

func (w waitingReleases) WaitingReleases(context.Context) ([]deployment.WaitingRelease, error) {
	return w, nil
}

// All checks' head counts every confirmed condition, and each row counts its
// own; a waiting release approval belongs to a row like any other condition.
func TestEveryConfirmedConditionIsCountedUnderOneSource(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	release := deployment.Release{ID: ids.New(now), Number: 17, CreatedAt: now.Format(time.RFC3339Nano)}
	disks := &instanceSource{snapshot: Snapshot{Known: true, ObservedAt: now, Items: []Item{{Key: "full", Title: "disk full"}}}}
	svc := New(&authority{instance: true}, Sources{
		ReleaseApprovals: Approvals{Releases: waitingReleases{{Release: release, DeploymentLabel: "Production", Project: "club"}}, Now: func() time.Time { return now }},
		Disks:            disks,
	})
	got, err := svc.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	confirmed, counted := 0, 0
	for _, item := range got.Items {
		if !item.Unknown {
			confirmed++
		}
	}
	for _, s := range got.Sources {
		counted += s.Conditions
	}
	if confirmed != 2 || counted != confirmed {
		t.Fatalf("confirmed %d, counted under sources %d: %+v", confirmed, counted, got.Sources)
	}
	for _, s := range got.Sources {
		if s.Key == "release-approval" && (s.Unknown || s.Conditions != 1 || !s.ObservedAt.Equal(now)) {
			t.Fatalf("approvals source: %+v", s)
		}
	}
	found := false
	for _, item := range got.Items {
		found = found || item.Key == "release-approval/"+release.ID.String()
	}
	if !found {
		t.Fatalf("item keys: %+v", got.Items)
	}
}
