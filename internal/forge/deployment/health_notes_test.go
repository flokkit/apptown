package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
)

// The controller writes history against the generation it acted on, even
// when a newer generation has released before the best-effort record arrives.
func TestACrashLoopNoteNamesTheReleasedGeneration(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint("stop-unconfirmed=", failed), func(t *testing.T) {
			t.Parallel()
			f, dep, releaseID, at := releaseOutcomeFixture(t)
			if _, err := f.outcomes.Complete(t.Context(), deploymentCompletionFor(at, "first")); err != nil {
				t.Fatal(err)
			}
			settled := storedRelease(t, f.db, dep.ProjectID, releaseID)
			releases := NewReleases(f.db)
			next, err := releases.Start(t.Context(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: "main", Revision: "later", Actor: "alice"}, "later release")
			if err != nil {
				t.Fatal(err)
			}
			later, _, err := f.outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "later", Inputs: Inputs{ReleaseID: next.ID, Revision: "later"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.outcomes.Complete(t.Context(), deploymentCompletionFor(later, "later")); err != nil {
				t.Fatal(err)
			}
			before, err := releases.Events(t.Context(), releaseID)
			if err != nil {
				t.Fatal(err)
			}
			currentBefore, err := releases.Events(t.Context(), next.ID)
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{db: f.db}
			// The slug is deliberately not resolvable: attribution uses captured IDs.
			w := docker.Watched{Slug: "a-name-that-is-not-the-project", Name: dep.Name, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Identity: deploy.DockerIdentity{Deployment: dep.Label(), Generation: at.Generation}}
			s.recordHealth(t.Context(), w, "deployment.crash-loop-held", map[string]any{"container": "first", "containerId": "exact-container", "restarts": 5, "window": "10m0s", "ok": !failed, "detail": "daemon answer unavailable"})
			events, err := releases.Events(t.Context(), releaseID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != len(before)+1 || !strings.Contains(events[len(events)-1].Text, "After release: the app crash-looped") {
				t.Fatalf("missing post-release note: %+v", events)
			}
			text := events[len(events)-1].Text
			if failed && (!strings.Contains(text, "stop could not be confirmed") || strings.Contains(text, "Forge stopped")) {
				t.Fatalf("unconfirmed stop claimed success: %s", text)
			}
			if !failed && !strings.Contains(text, "Forge stopped its container") {
				t.Fatalf("confirmed stop: %s", text)
			}
			current, err := releases.Events(t.Context(), next.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(current) != len(currentBefore) {
				t.Fatalf("older hold was written on newer release: %+v", current)
			}
			got := storedRelease(t, f.db, dep.ProjectID, releaseID)
			if !reflect.DeepEqual(got, settled) || got.State != Released || got.Generation != at.Generation {
				t.Fatalf("release success rewritten: %+v", got)
			}
			entries, err := audit.Recent(t.Context(), f.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.crash-loop-held"})
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].ProjectID != dep.ProjectID {
				t.Fatalf("audit attribution: %+v", entries)
			}
			var payload struct {
				DeploymentID, ReleaseID ids.ID
				Generation              int64
				ContainerID             string
				OK                      bool
			}
			if err := json.Unmarshal(entries[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.DeploymentID != dep.ID || payload.ReleaseID != releaseID || payload.Generation != at.Generation || payload.ContainerID != "exact-container" || payload.OK == failed {
				t.Fatalf("exact receipt: %+v", payload)
			}
			// Another placement identity under the same name must not acquire this release.
			w.DeploymentID = ids.New(s.now())
			s.recordHealth(t.Context(), w, "deployment.crash-loop-held", map[string]any{"ok": true})
			unchanged, err := releases.Events(t.Context(), releaseID)
			if err != nil {
				t.Fatal(err)
			}
			if len(unchanged) != len(events) {
				t.Fatalf("recreated same-name placement took old release: %+v", unchanged)
			}
			entries, err = audit.Recent(t.Context(), f.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.crash-loop-held"})
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatalf("generation without release lost audit: %+v", entries)
			}
			w.DeploymentID = dep.ID
			w.Identity.Generation = at.Generation + 100
			s.recordHealth(t.Context(), w, "deployment.crash-loop-held", map[string]any{"ok": true})
			unchanged, err = releases.Events(t.Context(), releaseID)
			if err != nil {
				t.Fatal(err)
			}
			if len(unchanged) != len(events) {
				t.Fatalf("unmatched generation took this release: %+v", unchanged)
			}
			entries, err = audit.Recent(t.Context(), f.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.crash-loop-held"})
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 3 {
				t.Fatalf("unmatched generation lost audit: %+v", entries)
			}
			var unbound map[string]any
			if err = json.Unmarshal(entries[0].Payload, &unbound); err != nil {
				t.Fatal(err)
			}
			if _, ok := unbound["releaseId"]; ok {
				t.Fatalf("fabricated release relation: %+v", unbound)
			}
		})
	}
}

// Failed persistence must not publish half a note/receipt, nor change the
// released generation's settlement because the container was already stopped.
func TestACrashLoopNoteAndReceiptPersistTogether(t *testing.T) {
	t.Parallel()
	f, dep, releaseID, at := releaseOutcomeFixture(t)
	if _, err := f.outcomes.Complete(t.Context(), deploymentCompletionFor(at, "first")); err != nil {
		t.Fatal(err)
	}
	before, err := NewReleases(f.db).Events(t.Context(), releaseID)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, f.db, "INSERT INTO audit_log")
	s := &Service{db: f.db}
	s.recordHealth(context.Background(), docker.Watched{ProjectID: dep.ProjectID, DeploymentID: dep.ID, Identity: deploy.DockerIdentity{Generation: at.Generation}}, "deployment.crash-loop-held", map[string]any{"ok": true})
	after, err := NewReleases(f.db).Events(t.Context(), releaseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("partial history survived: %+v", after)
	}
	if got := storedRelease(t, f.db, dep.ProjectID, releaseID); got.State != Released {
		t.Fatalf("receipt failure changed release state: %+v", got)
	}
}
