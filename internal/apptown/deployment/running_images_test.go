package deployment

import (
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

// Running-image reads use the serving attempt, not a desired bundle, and keep
// a separate error row when another deployment's running software is unknown.
func TestRunningImagesReadTheServingBundleAndKeepUnknownRows(t *testing.T) {
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	dep := w.mainOf(t, b)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "unknown", w.lab.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	rows, err := w.s.RunningImages(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("deployment rows: %+v", rows)
	}
	for _, row := range rows {
		if row.DeploymentID == dep.ID {
			if row.BundleID != b.ID || row.Reason != "" || len(row.Images) != len(b.Parts)+len(b.Init) {
				t.Fatalf("serving bundle: %+v", row)
			}
		} else if row.Reason == "" || !row.BundleID.IsZero() {
			t.Fatalf("unknown became clean: %+v", row)
		}
	}
}

func TestRunningImagesCannotJudgeABundleOfLocalImageIDs(t *testing.T) {
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	w.mainOf(t, b)
	// Cut's local form records a target rather than a registry. Reusing the
	// images already on that target exercises the same public release path.
	id, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, b.Parts, b.Init, w.contract, BundleOrigin{TargetID: w.lab.ID, RunID: ids.New(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	local, err := w.s.Bundles.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	w.mainOf(t, local)
	rows, err := w.s.RunningImages(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].BundleID != id || rows[0].Reason == "" || len(rows[0].Images) != 0 {
		t.Fatalf("local IDs became a judged image: %+v", rows)
	}
}
