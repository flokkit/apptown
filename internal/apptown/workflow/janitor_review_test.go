package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestReviewIncompleteSourceCannotClassifyRunningFindings(t *testing.T) {
	for _, receiptFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("receipt-first-%v", receiptFirst), func(t *testing.T) {
			t.Parallel()
			s, ch, repo, _, running := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch, "scan", "scan")
			steps, err := s.RunSteps(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The expanded source scan has two scanners. Gitleaks has answered;
			// the dependency scanner still has no answer. No raw storage writes.
			owner := newStepAttempts(s.db, func(string) bool { return true })
			claim, err := owner.start(t.Context(), steps[0], "scan", ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.finish(t.Context(), claim, StepOutcome{Status: StatusPass}); err != nil {
				t.Fatal(err)
			}
			pending, err := owner.start(t.Context(), steps[1], "scan", ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			dep, bundle := idstest.N(80), idstest.N(81)
			running.rows = []RunningImages{{DeploymentID: dep, BundleID: bundle, Images: map[string]string{"web": "digest-a"}}}
			finding := conformance.Finding{Finding: check.Finding{Identifiers: []string{"CVE-NPM"}, Package: &check.PackageRef{Name: "lodash", Ecosystem: "npm"}}}
			observations := []conformance.ScanObservation{
				{RunID: run.ID, StepID: steps[0].ID, Subject: conformance.ScanSubject{On: "source", Commit: gittest.Run(t, repo, "rev-parse", "HEAD")}, ExpectedScanners: []string{"gitleaks", "osv"}, Scanner: "gitleaks", Verdict: "pass", ObservedAt: now},
				{RunID: idstest.N(82), StepID: idstest.N(83), Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest-a"}, Scanner: "trivy", Verdict: "fail", ObservedAt: now.Add(-time.Minute), Findings: []conformance.Finding{finding}},
			}
			for _, o := range observations {
				o.ID, o.ProjectID, o.ChangeID, o.Attempt = ids.New(o.ObservedAt), ch.ProjectID, ch.ID, 1
				if err := s.db.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, o) }); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Known || len(got.Running[0].ImageOnlyFindings) != 0 || len(got.Running[0].UnclassifiedFindings) != 1 {
				t.Fatalf("unfinished source scan classified running findings: known=%v imageOnly=%d unclassified=%d", got.Known, len(got.Running[0].ImageOnlyFindings), len(got.Running[0].UnclassifiedFindings))
			}
			settle := func() {
				if err := owner.finish(t.Context(), pending, StepOutcome{Status: StatusPass}); err != nil {
					t.Fatal(err)
				}
			}
			record := func() {
				o := conformance.ScanObservation{ID: ids.New(now), ProjectID: ch.ProjectID, ChangeID: ch.ID, RunID: run.ID, StepID: steps[1].ID, Attempt: 1, Subject: observations[0].Subject, ExpectedScanners: []string{"gitleaks", "osv"}, Scanner: "osv", Verdict: "pass", ObservedAt: now}
				if err := s.db.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, o) }); err != nil {
					t.Fatal(err)
				}
			}
			if receiptFirst {
				record()
			} else {
				settle()
			}
			got, err = s.LatestMainScans(t.Context(), ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			// A usable retained answer is sufficient even before step settlement;
			// settlement alone cannot substitute for the missing answer.
			if got.Known != receiptFirst {
				t.Fatal("source completeness must follow retained answers")
			}
			if receiptFirst {
				settle()
			} else {
				record()
			}
			got, err = s.LatestMainScans(t.Context(), ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Known || len(got.Running[0].ImageOnlyFindings) != 1 || len(got.Running[0].UnclassifiedFindings) != 0 {
				t.Fatalf("complete source answer did not classify: %+v", got)
			}
		})
	}
}

func TestReviewSourceWithNothingToJudgeIsNotKnown(t *testing.T) {
	t.Parallel()
	s, ch, repo, _, running := automatedFixture(t)
	now := time.Now().UTC()
	dep, bundle := idstest.N(90), idstest.N(91)
	running.rows = []RunningImages{{DeploymentID: dep, BundleID: bundle, Images: map[string]string{"web": "digest-a"}}}
	finding := conformance.Finding{Finding: check.Finding{Identifiers: []string{"CVE-NPM"}, Package: &check.PackageRef{Name: "lodash", Ecosystem: "npm"}}}
	// RunScan records inability to judge as error for answer.empty
	// (the scanner's declared EmptyExit, e.g. OSV's exit 128).
	recordObservations(t, s, ch, []conformance.ScanObservation{
		{RunID: idstest.N(92), Subject: conformance.ScanSubject{On: "source", Commit: gittest.Run(t, repo, "rev-parse", "HEAD")}, Scanner: "osv", Verdict: "error", Reason: "osv found nothing to judge", ObservedAt: now},
		{RunID: idstest.N(93), Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest-a"}, Scanner: "trivy", Verdict: "fail", ObservedAt: now, Findings: []conformance.Finding{finding}},
	})
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || len(got.Running[0].ImageOnlyFindings) != 0 || len(got.Running[0].UnclassifiedFindings) != 1 {
		t.Fatalf("nothing-to-judge source classified running findings: known=%v imageOnly=%d unclassified=%d", got.Known, len(got.Running[0].ImageOnlyFindings), len(got.Running[0].UnclassifiedFindings))
	}
}

func TestReviewQuietQueueFinishesAfterFinding(t *testing.T) {
	for _, queue := range []bool{true, false} {
		for _, work := range []bool{false, true} {
			for _, failed := range []string{"observe-0", "observe-1"} {
				for _, verdict := range []StepState{StatusFail, StatusError} {
					t.Run(fmt.Sprintf("queue-%v/work-%v/%s/%s", queue, work, failed, verdict), func(t *testing.T) {
						t.Parallel()
						fixture, ch, _, retention, running := automatedFixture(t)
						if !queue {
							if err := fixture.db.InTx(t.Context(), func(tx *database.Tx) error { return promoteChangeIn(t.Context(), tx, ch.ProjectID, ch.ID) }); err != nil {
								t.Fatal(err)
							}
						}
						calls := 0
						runner := func(e *Execution) {
							if _, err := e.Claim(); err != nil {
								t.Error(err)
								return
							}
							calls++
							status := StatusPass
							if e.Step().Name == failed {
								status = verdict
							}
							if err := e.Settle(EncodeOutcome(status, map[string]any{"reason": "observed findings"}, nil)); err != nil {
								t.Error(err)
							}
						}
						s := New(context.Background(), fixture.db, Dependencies{
							Merge:   mergeEffectsTest{},
							DataDir: fixture.dataDir, Repositories: &scm.RepositoryLocks{},
							Abandonment: abandonRecorderFunc(noAbandonEffects), Retention: retention, ScanSchedules: scheduledScans{}, RunningImages: running,
							Automatic: []string{"scan", "script"}, Runners: map[string]Runner{"scan": runner, "script": runner},
						})
						s.authority = allowProject(ch.ProjectID)
						defer s.Stop()
						kinds := []string{"scan", "scan"}
						if work {
							kinds = []string{"scan", "script", "scan"}
						}
						run := admitAutomatedRun(t, s, ch, kinds...)
						if err := s.DispatchStarted(run.ID); err != nil {
							t.Fatal(err)
						}
						s.Wait()
						current, err := s.Change(t.Context(), ch.ID)
						if err != nil {
							t.Fatal(err)
						}
						attempts, err := s.Attempts(t.Context(), ch.ProjectID, "security", 20)
						if err != nil {
							t.Fatal(err)
						}
						if !queue || work {
							want := 1
							if failed == "observe-1" {
								want = 2
							}
							if calls != want || current.State.Closed() {
								t.Fatalf("ordinary run continued or closed: calls=%d change=%+v", calls, current)
							}
							return
						}
						if calls != 2 || current.ClosedAs != ClosedNothingToChange || attempts[0].Outcome != verdict.String() {
							t.Fatalf("quiet queue did not finish: scans=%d state=%s closedAs=%s outcome=%q", calls, current.State, current.ClosedAs, attempts[0].Outcome)
						}
					})
				}
			}
		}
	}
}
