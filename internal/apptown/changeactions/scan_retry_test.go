package changeactions_test

import (
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestFailedScansRetryHourlyThroughTheirSchedule(t *testing.T) {
	for _, verdict := range []string{"error", "pass", "fail", "stale"} {
		t.Run(verdict, func(t *testing.T) {
			t.Parallel()
			w := newScheduleWorld(t)
			body := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: source-clock}
spec:
  kind: pipeline
  on:
    schedule: {cron: "0 5 * * *"}
  stages:
    - name: observe
      steps:
        - name: source
          uses: scan
          with: {on: source}
`
			if _, _, err := w.ways.PublishWorkflow(t.Context(), body, "fixture"); err != nil {
				t.Fatal(err)
			}
			w.packWorkflow(t, w.project.ID, "source-clock")
			if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "fixture"); err != nil {
				t.Fatal(err)
			}
			if _, err := w.adoption.RecordCurrent(t.Context(), w.project.ID, adoption.AdoptionEdit, "select scanner", "fixture"); err != nil {
				t.Fatal(err)
			}
			repo, err := w.project.Repository(w.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, repo, "init", "-q", "-b", "main")
			gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "source")
			if err := w.schedules.Projects.SetDefaultBranch(t.Context(), w.project.ID, "main"); err != nil {
				t.Fatal(err)
			}
			base := w.work.Now().UTC().Truncate(24 * time.Hour).Add(5 * time.Hour)
			at, result := base, verdict
			if verdict == "stale" {
				at = base.Add(-4 * 24 * time.Hour)
				result = "pass"
			}
			o := conformance.ScanObservation{ID: ids.New(at), ProjectID: w.project.ID, RunID: ids.New(at), StepID: ids.New(at), Attempt: 1,
				Subject: conformance.ScanSubject{On: "source", Commit: gittest.Run(t, repo, "rev-parse", "HEAD"), Tree: gittest.Run(t, repo, "rev-parse", "HEAD^{tree}")},
				Scanner: "osv", Verdict: result, ObservedAt: at, Reason: "registry unavailable"}
			if verdict == "fail" {
				o.Findings = []conformance.Finding{{Finding: check.Finding{Identifiers: []string{"CVE-TEST"}}}}
			}
			if err := w.schedules.DB.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, o) }); err != nil {
				t.Fatal(err)
			}
			scans, err := w.work.LatestMainScans(w.engine, w.project.ID, 0)
			if err != nil || scans.Failed != (verdict == "error") {
				t.Fatalf("scan state: %+v %v", scans, err)
			}
			if err := workflow.ArmScheduleIn(t.Context(), w.schedules.DB, w.project.ID, "source-clock", base); err != nil {
				t.Fatal(err)
			}
			if err := workflow.RecordScheduleFiringIn(t.Context(), w.schedules.DB, w.project.ID, "source-clock", base, workflow.ScheduleSkipped, ids.Nil, ids.Nil, 0); err != nil {
				t.Fatal(err)
			}
			view, err := w.schedules.Of(w.engine, w.project, base.Add(59*time.Minute))
			next := base.Add(24 * time.Hour)
			if verdict == "error" {
				next = base.Add(time.Hour)
			}
			if err != nil || len(view.Schedules) != 1 || !view.Schedules[0].Next.Equal(next) || view.Schedules[0].Due {
				t.Fatalf("next scheduled assessment: %+v, want %s: %v", view, next, err)
			}
			for _, elapsed := range []time.Duration{59 * time.Minute, time.Hour, 119 * time.Minute, 2 * time.Hour, 24 * time.Hour} {
				w.schedules.FireDueSchedules(w.engine, base.Add(elapsed))
				records, err := workflow.ScheduleRecordsIn(t.Context(), w.schedules.DB, w.project.ID)
				want := base
				if elapsed == 24*time.Hour {
					want = base.Add(elapsed)
				}
				if verdict == "error" && elapsed >= time.Hour {
					want = base.Add(elapsed / time.Hour * time.Hour)
				}
				if err != nil || !records["source-clock"].At.Equal(want) {
					t.Fatalf("at +%s firing=%+v want=%s: %v", elapsed, records["source-clock"], want, err)
				}
			}
		})
	}
}
