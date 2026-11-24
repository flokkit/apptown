package deployment

import (
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
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
	id, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, b.Parts, b.Init, w.contract, BundleOrigin{TargetID: w.lab.ID, RunID: ids.New(time.Now()), Needs: map[string]string{"postgres": "postgres:16.16-alpine@sha256:" + strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	local, err := w.s.Bundles.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	dep := w.mainOf(t, local)
	plan, err := w.s.prepareRebuild(t.Context(), dep, dep.ServingAttemptID)
	if err != nil || plan.bundle != nil || plan.needs["postgres"] != local.BuiltFrom.Needs["postgres"] {
		t.Fatalf("source repair lost generation need builds: %+v %v", plan, err)
	}
	owner := w.s.Deployments.StorageOf(dep)
	digest := "postgres@sha256:" + strings.Repeat("a", 64)
	if err := owner.Establish(t.Context(), deploy.StorageBinding{Need: "postgres", Volume: "test-postgres", DataPath: "/data", DeclaredImage: "postgres:16.15-alpine", ImageID: "sha256:" + strings.Repeat("b", 64), RepoDigest: digest, VolumeCreated: time.Now(), VolumeDriver: "local", Origin: "observed"}); err != nil {
		t.Fatal(err)
	}
	rows, err := w.s.RunningImages(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].BundleID != id || rows[0].Reason == "" || len(rows[0].Images) != 1 || rows[0].Images["need/postgres"] != "postgres:16.15-alpine@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("local IDs became a judged image: %+v", rows)
	}
}
