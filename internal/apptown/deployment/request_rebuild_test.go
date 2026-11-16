package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

type recordedRebuilder struct {
	releases []ids.ID
	commits  []string
	from     []int64
}

func (r *recordedRebuilder) StartRebuild(_ context.Context, _ ids.ID, release ids.ID, commit string, from int64) (ids.ID, error) {
	r.releases = append(r.releases, release)
	r.commits = append(r.commits, commit)
	r.from = append(r.from, from)
	return ids.New(time.Now()), nil
}

func TestRequestRebuildUsesTheServingCommitAndOrdinaryAutomaticAdmission(t *testing.T) {
	w, dep, bundle, _ := janitorReleaseWorld(t)
	rebuilder := &recordedRebuilder{}
	w.s.imageRebuilder = rebuilder
	queue := &routineDueQueue{times: map[ids.ID]time.Time{}}
	w.s.release.Queue = queue
	reason := "running scan found a new fixable advisory"
	rel, err := w.s.RequestRebuild(t.Context(), w.project, dep.Name, reason)
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Releases.ByID(t.Context(), rel.ID)
	if err != nil || got.BundleID != bundle.ID || got.CommitSHA != bundle.ContractRevision || got.State != Releasing {
		t.Fatalf("rebuild release: %+v %v", got, err)
	}
	meta, err := w.s.Releases.RequestMetadata(t.Context(), rel.ID)
	if err != nil || !meta.Routine || meta.Feed != "main" || meta.SettingsRevision != dep.FeedRevision || meta.SetBy == "" {
		t.Fatalf("automatic provenance: %+v %v", meta, err)
	}
	if _, ok := queue.times[rel.ID]; !ok {
		t.Fatal("rebuild bypassed the release queue")
	}
	if _, err := w.s.RequestRebuild(t.Context(), w.project, dep.Name, reason); !errors.Is(err, errReleaseInFlight) {
		t.Fatalf("second request was not refused: %v", err)
	}
	last, err := w.s.Releases.Last(t.Context(), w.project, dep.Name)
	if err != nil || last.ID != rel.ID {
		t.Fatalf("duplicate release: %+v %v", last, err)
	}
	if err := w.s.ExecuteRelease(t.Context(), rel.ID); err != nil {
		t.Fatal(err)
	}
	if len(rebuilder.releases) != 1 || rebuilder.releases[0] != rel.ID || rebuilder.commits[0] != bundle.ContractRevision {
		t.Fatalf("pinned run request: %+v", rebuilder)
	}
	events, err := w.s.Releases.Events(t.Context(), rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if strings.Contains(e.Text, reason) {
			found = true
		}
	}
	if !found {
		t.Fatalf("reason missing: %+v", events)
	}
}

func TestRequestRebuildRefusesManualFeedAndLeavesTheLedgerAlone(t *testing.T) {
	w, dep, _, first := janitorReleaseWorld(t)
	w.s.imageRebuilder = &recordedRebuilder{}
	if _, err := w.s.SetFeed(t.Context(), w.project, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RequestRebuild(t.Context(), w.project, dep.Name, "new finding"); !errors.Is(err, ErrRebuildNotApplicable) {
		t.Fatalf("manual feed admitted a rebuild: %v", err)
	}
	last, err := w.s.Releases.Last(t.Context(), w.project, dep.Name)
	if err != nil || last.ID != first.ID {
		t.Fatalf("manual feed changed ledger: %+v %v", last, err)
	}
}

func TestRequestRebuildHonoursRoutinePersonAndWindowSettings(t *testing.T) {
	for _, mode := range []string{"person", "window"} {
		t.Run(mode, func(t *testing.T) {
			w, dep, _, _ := janitorReleaseWorld(t)
			w.s.imageRebuilder = &recordedRebuilder{}
			now := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
			w.s.clock = func() time.Time { return now }
			queue := &routineDueQueue{times: map[ids.ID]time.Time{}}
			w.s.release.Queue = queue
			settings := RoutineUpdates{Mode: mode}
			if mode == "window" {
				settings.Days = "sun"
				settings.From = "02:00"
				settings.To = "04:00"
				settings.Zone = "UTC"
			}
			if _, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: settings}); err != nil {
				t.Fatal(err)
			}
			rel, err := w.s.RequestRebuild(t.Context(), w.project, dep.Name, "new advisory")
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.s.Releases.ByID(t.Context(), rel.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "person" {
				if got.State != releaseWaiting || len(queue.times) != 0 {
					t.Fatalf("person setting bypassed: %+v %+v", got, queue.times)
				}
			} else if !queue.times[rel.ID].Equal(now.Add(time.Hour)) {
				t.Fatalf("window bypassed: %+v", queue.times)
			}
		})
	}
}
