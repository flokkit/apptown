package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// A repair after a lost container reapplies the serving attempt at its
// generation: the deployment still serves its release in the listing, Live
// and the record reads, which carry the repair's finish time, and a release
// queued from the old base still executes.
//
// Kills: a repair that mints a generation or a release, a serving record
// left on the replaced attempt, a record read that selects by generation.
func TestARepairKeepsServingItsRelease(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	ctx := t.Context()
	b := w.publish(t)
	rel, err := w.promote(t, b.ID, "main", w.lab)
	if err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	before := mustDeployment(t, w, "main")
	queued, err := w.s.RequestRelease(ctx, DeploySelection{ProjectID: w.project, DeploymentName: "main", BundleID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.removeContainer(t, "lab", w.names(t, "main").App())
	if out, err := w.rebuild(t, before); err != nil || out["outcome"] != Applied {
		t.Fatalf("repair: %v %v", out, err)
	}
	after := mustDeployment(t, w, "main")
	if after.ServingReleaseID != rel.ID || after.ServingAttemptID == before.ServingAttemptID || after.ObservedGeneration != before.ObservedGeneration || after.DesiredGeneration != before.DesiredGeneration {
		t.Fatalf("serving record after the repair: %+v", after)
	}
	finished := storedAttempt(t, w.db, after.ServingAttemptID).FinishedAt.Time
	reads := map[string]*Deployment{"by id": after}
	if reads["by name"], err = w.s.Deployments.ByName(ctx, w.project, "main"); err != nil {
		t.Fatal(err)
	}
	project, err := w.s.Deployments.ForProject(ctx, w.project)
	if err != nil || len(project) != 1 {
		t.Fatalf("project deployments: %+v %v", project, err)
	}
	reads["for project"] = project[0]
	records, err := w.s.listRecords(ctx, []ids.ID{w.project})
	if err != nil || len(records) != 1 {
		t.Fatalf("runtime deployments: %+v %v", records, err)
	}
	reads["runtime"] = records[0].deployment
	for name, d := range reads {
		if d.LastApplied == nil || !d.LastApplied.At.Equal(finished) {
			t.Errorf("%s: last applied %+v, want the repair's %s", name, d.LastApplied, finished)
		}
	}
	summaries, err := w.s.List(ctx, w.project)
	if err != nil || len(summaries) != 1 || summaries[0].ServingRelease == nil || summaries[0].ServingRelease.ID != rel.ID {
		t.Fatalf("listing: %+v %v", summaries, err)
	}
	if live, err := w.s.Releases.Live(ctx, rel); err != nil || !live {
		t.Fatalf("the release is not live after its repair: %v %v", live, err)
	}
	none := func(context.Context, *Deployment, *Diagnostics) error { return nil }
	if d, err := w.s.diagnostics(ctx, w.project, "main", none); err != nil || d.ReleaseNumber != int(rel.Number) {
		t.Fatalf("diagnostics release: %d %v", d.ReleaseNumber, err)
	}
	// A repair job naming an attempt that does not serve does nothing.
	stale, err := JobWorker{Service: w.s, Projects: w.projects, DataDir: w.dir}.Rebuild(ctx, w.project.String()+"/main", map[string]any{"attempt": ids.New(time.Now())})
	if err != nil || !strings.Contains(fmt.Sprint(stale["skipped"]), "another attempt") {
		t.Fatalf("a stale repair: %v %v", stale, err)
	}
	if err := w.s.ExecuteRelease(ctx, queued.ID); err != nil {
		t.Fatalf("a release queued before the repair: %v", err)
	}
	if got, err := w.s.Releases.ByID(ctx, queued.ID); err != nil || got.State != Released {
		t.Fatalf("queued release after the repair: %+v %v", got, err)
	}
}

// Serving generation 1 after release 2 failed, a lost container is repaired
// through the Docker checkpoint at generation 1: desired stays 2 for the
// next decision, observation is no longer refused as a replacement in
// progress, and promotion and overrides still find the serving bundle.
//
// Kills: a checkpoint, settlement or observation that compares the attempt
// with the allocation counter; AdmitGeneration lowering desired; a running
// bundle gated on settled readiness.
func TestARepairAfterAFailedReleaseServesTheLastSuccessfulOne(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	ctx := t.Context()
	b := w.publish(t)
	first, err := w.promote(t, b.ID, "main", w.lab)
	if err != nil || first.State != Released {
		t.Fatalf("release: %+v %v", first, err)
	}
	failing := true
	w.hosts.daemon("lab").SetReply(func(args []string) ([]byte, error, bool) {
		if failing && len(args) > 0 && args[0] == "run" {
			return []byte("no room on the host"), errors.New("exit status 125"), true
		}
		return nil, nil, false
	})
	if second, err := w.promote(t, b.ID, "main", w.lab); err == nil || second.State == Released {
		t.Fatalf("the second release did not fail: %+v %v", second, err)
	}
	failed := mustDeployment(t, w, "main")
	if failed.DesiredGeneration != 2 || failed.ObservedGeneration != 1 || failed.Readiness != ReadinessFailed {
		t.Fatalf("after the failed release: %+v", failed)
	}
	if bundle, err := w.s.runningBundle(ctx, failed); err != nil || bundle != b.ID {
		t.Fatalf("the serving bundle after the failed release: %s %v", bundle, err)
	}
	failing = false
	w.removeContainer(t, "lab", w.names(t, "main").App())
	if out, err := w.rebuild(t, failed); err != nil || out["outcome"] != Applied {
		t.Fatalf("repair: %v %v\n%s", out, err, strings.Join(w.hosts.commands("lab"), "\n"))
	}
	repaired := mustDeployment(t, w, "main")
	if repaired.DesiredGeneration != 2 || repaired.ObservedGeneration != 1 || repaired.Readiness != Reconciled || repaired.ServingReleaseID != first.ID {
		t.Fatalf("after the repair: %+v", repaired)
	}
	if v, ok := w.s.observable(ctx, listingRecord{deployment: repaired, target: w.lab}); !ok {
		t.Fatalf("observation refused after the repair: %v", v.Err)
	}
	if bundle, err := w.s.runningBundle(ctx, repaired); err != nil || bundle != b.ID {
		t.Fatalf("overrides' running bundle: %s %v", bundle, err)
	}
	promoted := &Deployment{ProjectID: w.project, PromotedFromID: repaired.ID}
	if bundle, why, err := w.s.promotionBundle(ctx, promoted, ids.Nil); err != nil || bundle != b.ID || !strings.Contains(why, fmt.Sprintf("release %d ", first.Number)) {
		t.Fatalf("promotion source: %s %q %v", bundle, why, err)
	}
	if _, _, err := w.s.Outcomes.Admit(ctx, deploymentStartFor(repaired, "next", "next")); err != nil {
		t.Fatal(err)
	}
	if v, ok := w.s.observable(ctx, listingRecord{deployment: repaired, target: w.lab}); ok {
		t.Fatalf("observation asked while an attempt is open: %+v", v)
	}
}
