package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Advancing source never proves a running OS vulnerability was fixed. Only an
// exact package and advisory match in a usable main scan makes it the project's.
func TestRunningFindingsRemainBoundToTheirScannedSoftware(t *testing.T) {
	now := time.Now().UTC()
	dep, bundle, sourceRun, imageRun := idstest.N(1), idstest.N(2), idstest.N(3), idstest.N(4)
	osFinding := conformance.Finding{Finding: check.Finding{Identifiers: []string{"CVE-OS"}, Package: &check.PackageRef{Name: "libssl", Ecosystem: "Debian"}}}
	npmFinding := conformance.Finding{Finding: check.Finding{Identifiers: []string{"CVE-NPM"}, Package: &check.PackageRef{Name: "lodash", Ecosystem: "npm"}}}
	s, ch, repo, _, runningState := automatedFixture(t)
	s.scanSchedules = noScanSchedule{}
	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	running := []RunningImages{{DeploymentID: dep, BundleID: bundle, Images: map[string]string{"web": "digest-a"}}}
	observations := []conformance.ScanObservation{
		{RunID: sourceRun, Subject: conformance.ScanSubject{On: "source", Commit: main}, Scanner: "osv", Verdict: "fail", ObservedAt: now, Findings: []conformance.Finding{npmFinding}},
		{RunID: imageRun, Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest-a"}, Scanner: "trivy", Verdict: "fail", ObservedAt: now, Findings: []conformance.Finding{osFinding, npmFinding}},
	}
	runningState.rows = running
	recordObservations(t, s, ch, observations)
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Known || got.Unscheduled || len(got.Running) != 1 || len(got.Running[0].ProjectFindings) != 1 || len(got.Running[0].ImageOnlyFindings) != 1 {
		t.Fatalf("split: %+v", got)
	}
	if got.Running[0].ProjectFindings[0].Package.Name != "lodash" || got.Running[0].ImageOnlyFindings[0].Package.Name != "libssl" {
		t.Fatalf("misclassified: %+v", got.Running[0])
	}
	// Main moves independently of the deployment. Its next source scan still
	// cannot account for an OS finding in the unchanged running image.
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "unrelated landing")
	observations[0].Subject.Commit = gittest.Run(t, repo, "rev-parse", "HEAD")
	observations[0].RunID = idstest.N(6)
	observations[0].ObservedAt = now.Add(time.Second)
	recordObservations(t, s, ch, observations[:1])
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Known || len(got.Running[0].ImageOnlyFindings) != 1 || got.Running[0].ImageOnlyFindings[0].Package.Name != "libssl" {
		t.Fatalf("unrelated landing reclassified the running OS finding: %+v", got)
	}
	observations[0].Verdict = "error"
	observations[0].RunID = idstest.N(5)
	observations[0].ObservedAt = now.Add(2 * time.Second)
	recordObservations(t, s, ch, observations[:1])
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || len(got.Running[0].ProjectFindings) != 0 || len(got.Running[0].ImageOnlyFindings) != 0 || len(got.Running[0].UnclassifiedFindings) != 2 {
		t.Fatalf("an unavailable source classified findings: %+v", got)
	}
	// A new commit and a different serving image leave the old observations
	// historical, never a clean observation of the new subjects.
	running[0].Images["web"] = "digest-b"
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "unrelated landing")
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || len(got.Source) != 0 || len(got.Running) != 0 || len(got.Historical) != 4 {
		t.Fatalf("old software became current: %+v", got)
	}
}

func TestScansOlderThanThreeDaysAreNotKnown(t *testing.T) {
	now := time.Now().UTC()
	s, ch, repo, _, _ := automatedFixture(t)
	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	o := conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: main}, Scanner: "osv", Verdict: "pass", ObservedAt: now.Add(-73 * time.Hour)}
	recordObservations(t, s, ch, []conformance.ScanObservation{o})
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || !got.Stale {
		t.Fatalf("old scan: %+v", got)
	}
}

func TestEmptyDeterministicWorkRequiresEveryStepToPass(t *testing.T) {
	for _, state := range []StepState{StatusPass, StatusFail, StatusError, StatusPending, StatusRunning} {
		t.Run(state.String(), func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			if err := s.db.InTx(t.Context(), func(tx *database.Tx) error { return promoteChangeIn(t.Context(), tx, ch.ProjectID, ch.ID) }); err != nil {
				t.Fatal(err)
			}
			run := admitAutomatedRun(t, s, ch, "script", "scan")
			settleScanTestSteps(t, s, run, state)
			err := s.endNothingToChange(t.Context(), run)
			current, readErr := s.Change(t.Context(), ch.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if state == StatusPass {
				if err != nil || current.ClosedAs != ClosedNothingToChange {
					t.Fatalf("empty deterministic work did not close: %+v %v", current, err)
				}
			} else if !errors.Is(err, ErrRunMoved) || current.State.Closed() {
				t.Fatalf("closed with %s: %+v %v", state, current, err)
			}
		})
	}
}

func TestQuietQueuesKeepTheirActualOutcome(t *testing.T) {
	for _, startedWork := range []bool{false, true} {
		for _, state := range []StepState{StatusPass, StatusFail, StatusError, StatusNotApplicable} {
			t.Run(fmt.Sprintf("%s/work-%v", state, startedWork), func(t *testing.T) {
				s, ch, _, _, _ := automatedFixture(t)
				kinds := []string{"scan"}
				if startedWork {
					kinds = append(kinds, "script")
				}
				run := admitAutomatedRun(t, s, ch, kinds...)
				settleScanTestSteps(t, s, run, state)
				err := s.endNothingToChange(t.Context(), run)
				current, readErr := s.Change(t.Context(), ch.ID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if startedWork && (state == StatusFail || state == StatusError) {
					if !errors.Is(err, ErrRunMoved) || current.State.Closed() {
						t.Fatalf("started work lost its failure: %+v %v", current, err)
					}
					return
				}
				if err != nil || current.ClosedAs != ClosedNothingToChange {
					t.Fatalf("quiet queue did not close: %+v %v", current, err)
				}
				attempts, err := s.Attempts(t.Context(), ch.ProjectID, "security", 20)
				want := "pass"
				if state == StatusFail || state == StatusError {
					want = state.String()
				}
				if err != nil || len(attempts) != 1 || attempts[0].Outcome != want {
					t.Fatalf("%s became %+v: %v", state, attempts, err)
				}
			})
		}
	}
}

func settleScanTestSteps(t *testing.T, s *Service, run *Run, state StepState) {
	t.Helper()
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := newStepAttempts(s.db, func(string) bool { return true })
	for _, step := range steps {
		verdict := StatusPass
		if step.Type == "scan" {
			verdict = state
		}
		if verdict == StatusPending {
			continue
		}
		claim, err := owner.start(t.Context(), step, step.Type, run.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		if verdict == StatusRunning {
			continue
		}
		if err := owner.finish(t.Context(), claim, StepOutcome{Status: verdict, Evidence: json.RawMessage(`{"reason":"scan result"}`)}); err != nil {
			t.Fatal(err)
		}
	}
}

func recordObservations(t *testing.T, s *Service, ch *Change, observations []conformance.ScanObservation) {
	t.Helper()
	runs := map[ids.ID]*Run{}
	steps := map[ids.ID]*Step{}
	for _, o := range observations {
		if o.Subject.On == "source" {
			if runs[o.RunID] == nil {
				run := admitAutomatedRun(t, s, ch, "scan")
				settleScanTestSteps(t, s, run, StatusPass)
				rows, err := s.RunSteps(t.Context(), run.ID)
				if err != nil {
					t.Fatal(err)
				}
				runs[o.RunID], steps[o.RunID] = run, rows[0]
				// Release admission for the next fixture run without closing its Change.
				// Normal completion here would close the empty Change as well.
				transitionStoredRunForTest(t, s.db, run.ID, "", RunDone)
			}
			o.StepID, o.RunID = steps[o.RunID].ID, runs[o.RunID].ID
			if o.ExpectedScanners == nil {
				o.ExpectedScanners = []string{o.Scanner}
			}
		} else {
			o.StepID = o.RunID
		}
		o.ID = ids.New(o.ObservedAt)
		o.ProjectID, o.ChangeID = ch.ProjectID, ch.ID
		o.Attempt = 1
		if err := s.db.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, o) }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceCompletenessUsesOnlyRetainedExpectedScanners(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected []string
		verdict  string
		peer     bool
		known    bool
	}{
		{"missing set", nil, "pass", false, false},
		{"explicit empty set", []string{}, "pass", false, false},
		{"unattached", []string{""}, "pass", false, false},
		{"unfinished peer", []string{"osv", "gitleaks"}, "pass", false, false},
		{"error answer", []string{"osv", "gitleaks"}, "error", true, false},
		{"unusable verdict", []string{"osv"}, "pending", false, false},
		{"all answers", []string{"osv", "gitleaks"}, "fail", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observations := []conformance.ScanObservation{{Scanner: "osv", ExpectedScanners: tc.expected, Verdict: tc.verdict}}
			if tc.peer {
				observations = append(observations, conformance.ScanObservation{Scanner: "gitleaks", ExpectedScanners: tc.expected, Verdict: "pass"})
			}
			if _, got := unansweredScanners(observations); got != tc.known {
				t.Fatalf("complete=%v, want %v", got, tc.known)
			}
		})
	}
}

// A landing scans only the commits it adds with gitleaks, so for gitleaks its
// run says nothing about the whole code. On the demo, landings whose main did
// not move hid the complete scheduled scan of that same commit as "scan not
// finished".
func TestALandingScanTakesItsHistoryScannerFromTheLastWholeScan(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	both := []string{"osv", "gitleaks"}
	for _, tc := range []struct {
		name       string
		earlier    bool
		history    bool
		landingOSV string
		donor      string        // the earlier whole scan's gitleaks verdict
		donorAge   time.Duration // and how long ago it was observed
		known      bool
		reason     string
	}{
		{"an earlier whole scan answers gitleaks", true, true, "pass", "pass", 3 * time.Hour, true, ""},
		{"the landing's own error stays current", true, true, "error", "pass", 3 * time.Hour, false, "osv could not read its database"},
		{"no earlier whole scan of gitleaks", false, true, "pass", "pass", 3 * time.Hour, false, "gitleaks gave no result for the current code"},
		// The earlier answer keeps its own verdict and age.
		{"an earlier gitleaks error stays an error", true, true, "pass", "error", 3 * time.Hour, false, "gitleaks could not run"},
		{"an earlier gitleaks answer keeps its age", true, true, "pass", "pass", 80 * time.Hour, false, "the last scan was on"},
		// A scanner that never answered is unfinished, not a history scan:
		// an older answer does not stand in for it.
		{"an unfinished scanner is not filled", true, false, "pass", "pass", 3 * time.Hour, false, "gitleaks gave no result for the current code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, ch, repo, _, _ := automatedFixture(t)
			main := gittest.Run(t, repo, "rev-parse", "HEAD")
			var observations []conformance.ScanObservation
			if tc.earlier {
				observations = append(observations,
					conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: main}, ExpectedScanners: both, Scanner: "osv", Verdict: "pass", ObservedAt: now.Add(-tc.donorAge)},
					conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: main}, ExpectedScanners: both, Scanner: "gitleaks", Verdict: tc.donor, Reason: map[string]string{"error": "gitleaks could not run"}[tc.donor], ObservedAt: now.Add(-tc.donorAge)})
			}
			landingOSV := conformance.ScanObservation{RunID: idstest.N(2), Subject: conformance.ScanSubject{On: "source", Commit: main}, ExpectedScanners: both, Scanner: "osv", Verdict: tc.landingOSV, ObservedAt: now.Add(-time.Hour)}
			if tc.landingOSV == "error" {
				landingOSV.Reason = "osv could not read its database"
			}
			observations = append(observations,
				conformance.ScanObservation{RunID: idstest.N(3), Subject: conformance.ScanSubject{On: "running"}, Verdict: StatusNotApplicable.String(), ObservedAt: now.Add(-time.Hour)},
				landingOSV)
			if tc.history {
				observations = append(observations, conformance.ScanObservation{RunID: idstest.N(2), Subject: conformance.ScanSubject{On: "source", Commit: main, History: main + ".." + main}, ExpectedScanners: both, Scanner: "gitleaks", Verdict: "pass", ObservedAt: now.Add(-time.Hour)})
			}
			recordObservations(t, s, ch, observations)
			got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Known != tc.known || !strings.HasPrefix(got.Reason, tc.reason) || tc.reason == "" && got.Reason != "" {
				t.Fatalf("known=%v reason=%q, want %v %q; source=%+v", got.Known, got.Reason, tc.known, tc.reason, got.Source)
			}
			for _, o := range got.Source {
				if o.Subject.History != "" {
					t.Fatalf("a history observation became current: %+v", o)
				}
				// The landing's own whole-tree osv stays its answer; only
				// gitleaks comes from the earlier scan.
				if o.Scanner == "osv" && o.Verdict != tc.landingOSV || o.Scanner == "gitleaks" && (o.Verdict != tc.donor || !o.ObservedAt.Before(now.Add(-2*time.Hour))) {
					t.Fatalf("wrong answer for %s: %+v", o.Scanner, o)
				}
			}
		})
	}
}

// Each reason names its case and subject, so a reader knows what a new scan
// has to answer.
func TestNotKnownReasonsNameTheirCase(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	dep, bundle := idstest.N(10), idstest.N(11)
	running := []RunningImages{{DeploymentID: dep, BundleID: bundle, Deployment: "main", Images: map[string]string{"web": "digest-a"}}}
	const main = "5c406e206353120f72fb285835c7bf9b0cac0048"
	source := func(run uint64, scanner string, at time.Time) conformance.ScanObservation {
		return conformance.ScanObservation{RunID: idstest.N(run), Subject: conformance.ScanSubject{On: "source", Commit: main}, ExpectedScanners: []string{"osv", "gitleaks"}, Scanner: scanner, Verdict: "pass", ObservedAt: at}
	}
	image := conformance.ScanObservation{RunID: idstest.N(2), Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest-a"}, Scanner: "trivy", Verdict: "pass", ObservedAt: now}
	old := now.Add(-80 * time.Hour)
	for _, tc := range []struct {
		name         string
		observations []conformance.ScanObservation
		want         string
	}{
		{"no source scan", []conformance.ScanObservation{image}, "the current code (5c406e206353) has not been scanned"},
		{"a scanner without a result", []conformance.ScanObservation{source(1, "osv", now), image}, "gitleaks gave no result for the current code (5c406e206353)"},
		{"running image not scanned", []conformance.ScanObservation{source(1, "osv", now), source(1, "gitleaks", now)}, "the image main runs has not been scanned since it was deployed"},
		{"older than three days", []conformance.ScanObservation{source(1, "osv", old), source(1, "gitleaks", old), image}, "the last scan was on " + old.Format("2 Jan") + ", more than three days ago"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := latestMainScans(main, running, tc.observations, now)
			if got.Known || got.Reason != tc.want {
				t.Fatalf("known=%v reason=%q, want %q", got.Known, got.Reason, tc.want)
			}
		})
	}
}

// Missing evidence is actionable only when the reader knows whether anything scans.
func TestUnknownScansSayWhenNoScheduleScansSource(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	s.scanSchedules = noScanSchedule{}
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil || !got.Unscheduled || got.Known || got.Reason != "its packs schedule no scan" {
		t.Fatalf("unscheduled: %+v %v", got, err)
	}
	s.scanSchedules = noScanSchedule{err: errors.New("cannot read schedule")}
	if _, err := s.LatestMainScans(t.Context(), ch.ProjectID); err == nil {
		t.Fatal("read failure became no schedule")
	}
}

type noScanSchedule struct{ err error }

func (s noScanSchedule) ScansSource(context.Context, ids.ID) (bool, error) { return false, s.err }
