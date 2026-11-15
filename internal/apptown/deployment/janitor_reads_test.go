package deployment

import (
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func TestJanitorDeploymentReadShowsCurrentSettingsAndReleaseHold(t *testing.T) {
	w, dep, base, _ := janitorReleaseWorld(t)
	w.s.authority = &requestAuthority{visible: []ids.ID{w.project}}
	rel := requestJanitorBundle(t, w, nextJanitorBundle(t, w, base))
	w.s.release.Sources = &requestSources{err: errors.New("cannot prepare")}
	if err := w.s.ExecuteRelease(t.Context(), rel.ID); err == nil {
		t.Fatal("fixture release did not fail")
	}
	rows, err := w.s.JanitorDeployments(t.Context(), []ids.ID{w.project})
	if err != nil || len(rows) != 1 || rows[0].Hold == nil || rows[0].Hold.ReleaseNumber != rel.Number {
		t.Fatalf("read=%+v %v", rows, err)
	}
	if rows[0].Name != dep.Name || rows[0].DisplayLabel != dep.DisplayLabel || !rows[0].Automatic {
		t.Fatalf("settings=%+v", rows[0])
	}
	if _, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: RoutineUpdates{Mode: "window", Days: "mon,wed", From: "09:00", To: "11:00", Zone: "Australia/Melbourne"}}); err != nil {
		t.Fatal(err)
	}
	rows, err = w.s.JanitorDeployments(t.Context(), []ids.ID{w.project})
	if err != nil || rows[0].Setting != (RoutineUpdates{Mode: "window", Days: "mon,wed", From: "09:00", To: "11:00", Zone: "Australia/Melbourne"}) {
		t.Fatalf("window=%+v %v", rows, err)
	}
	current, err := w.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, RoutineUpdateSettings{Revision: current.RoutineUpdatesRevision, RoutineUpdates: RoutineUpdates{Mode: "person"}}); err != nil {
		t.Fatal(err)
	}
	rows, err = w.s.JanitorDeployments(t.Context(), []ids.ID{w.project})
	if err != nil || rows[0].Automatic || rows[0].Hold != nil || rows[0].Setting.Mode != "person" {
		t.Fatalf("person=%+v %v", rows, err)
	}
	change := shippedChangeAt(t, w.db, w.project, time.Now().Add(-time.Hour))
	if _, err := w.s.Releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: w.project, DeploymentID: dep.ID, Deployment: dep.Name, BundleID: base.ID, Actor: "alice"}, "Update libraries"); err != nil {
		t.Fatal(err)
	}
	releases, err := w.s.JanitorChangeReleases(t.Context(), []ids.ID{w.project}, []ids.ID{change})
	if err != nil || len(releases) == 0 {
		t.Fatalf("releases=%+v %v", releases, err)
	}
	if releases[0].DisplayLabel != dep.DisplayLabel {
		t.Fatalf("release=%+v", releases[0])
	}
	w.s.authority = &requestAuthority{deny: errors.New("refused")}
	if _, err := w.s.JanitorDeployments(t.Context(), []ids.ID{w.project}); err == nil {
		t.Fatal("deployment read bypassed caller authority")
	}
}

func TestJanitorDeploymentSetRefusesAnyUnreadableProject(t *testing.T) {
	t.Parallel()
	s, authority, _, id := listingFixture(t, 0, absentObservation)
	// Require would permit the request: the set must use its own visibility answer.
	authority.projects = []ids.ID{id}
	if _, err := s.JanitorDeployments(t.Context(), []ids.ID{id, ids.New(time.Now())}); err == nil {
		t.Fatal("unreadable project in supplied set was trusted")
	}
}
