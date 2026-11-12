package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
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
	if !got.Known || len(got.Running) != 1 || len(got.Running[0].ProjectFindings) != 1 || len(got.Running[0].ImageOnlyFindings) != 1 {
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
			o.ExpectedScanners = []string{o.Scanner}
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
			if got := sourceScansComplete(observations); got != tc.known {
				t.Fatalf("complete=%v, want %v", got, tc.known)
			}
		})
	}
}
