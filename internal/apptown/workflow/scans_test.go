package workflow

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		{RunID: sourceRun, Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, Scanner: "osv", Verdict: "fail", ObservedAt: now, Findings: []conformance.Finding{npmFinding}},
		{RunID: imageRun, Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest-a"}, Scanner: "trivy", Verdict: "fail", ObservedAt: now, Findings: []conformance.Finding{osFinding, npmFinding}},
	}
	runningState.rows = running
	recordObservations(t, s, ch, observations)
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
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
	observations[0].Subject.Tree = gittest.Run(t, repo, "rev-parse", "HEAD^{tree}")
	observations[0].RunID = idstest.N(6)
	observations[0].ObservedAt = now.Add(time.Second)
	recordObservations(t, s, ch, observations[:1])
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID, 0)
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
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || len(got.Running[0].ProjectFindings) != 0 || len(got.Running[0].ImageOnlyFindings) != 0 || len(got.Running[0].UnclassifiedFindings) != 2 {
		t.Fatalf("an unavailable source classified findings: %+v", got)
	}
	// A new commit and a different serving image leave the old observations
	// historical, never a clean observation of the new subjects.
	running[0].Images["web"] = "digest-b"
	if err := os.WriteFile(filepath.Join(repo, "new-file"), []byte("new tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "new-file")
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "-qm", "unrelated landing")
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID, 10)
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
	o := conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, Scanner: "osv", Verdict: "pass", ObservedAt: now.Add(-73 * time.Hour)}
	recordObservations(t, s, ch, []conformance.ScanObservation{o})
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
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
		if o.ID.IsZero() {
			o.ID = ids.New(o.ObservedAt)
		}
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
					conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, ExpectedScanners: both, Scanner: "osv", Verdict: "pass", ObservedAt: now.Add(-tc.donorAge)},
					conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, ExpectedScanners: both, Scanner: "gitleaks", Verdict: tc.donor, Reason: map[string]string{"error": "gitleaks could not run"}[tc.donor], ObservedAt: now.Add(-tc.donorAge)})
			}
			landingOSV := conformance.ScanObservation{RunID: idstest.N(2), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, ExpectedScanners: both, Scanner: "osv", Verdict: tc.landingOSV, ObservedAt: now.Add(-time.Hour)}
			if tc.landingOSV == "error" {
				landingOSV.Reason = "osv could not read its database"
			}
			observations = append(observations,
				conformance.ScanObservation{RunID: idstest.N(3), Subject: conformance.ScanSubject{On: "running"}, Verdict: StatusNotApplicable.String(), ObservedAt: now.Add(-time.Hour)},
				landingOSV)
			if tc.history {
				observations = append(observations, conformance.ScanObservation{RunID: idstest.N(2), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}"), History: main + ".." + main}, ExpectedScanners: both, Scanner: "gitleaks", Verdict: "pass", ObservedAt: now.Add(-time.Hour)})
			}
			for i := range observations {
				observations[i].ID = ids.New(now)
				if observations[i].Scanner == "gitleaks" && observations[i].Subject.History == "" {
					observations[i].Findings = []conformance.Finding{{Finding: check.Finding{Fingerprint: "donor-finding"}}}
				}
			}
			recordObservations(t, s, ch, observations)
			got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got.Known != tc.known || !strings.HasPrefix(got.Reason, tc.reason) || tc.reason == "" && got.Reason != "" {
				t.Fatalf("known=%v reason=%q, want %v %q; source=%+v", got.Known, got.Reason, tc.known, tc.reason, got.Source)
			}
			if len(got.Historical) != 0 {
				t.Fatalf("earlier=0 decoded history: %+v", got.Historical)
			}
			withEarlier, err := s.LatestMainScans(t.Context(), ch.ProjectID, 10)
			if err != nil || !reflect.DeepEqual(got.Source, withEarlier.Source) || got.Known != withEarlier.Known || got.Reason != withEarlier.Reason {
				t.Fatalf("history changed current scan: %+v %+v %v", got, withEarlier, err)
			}
			for _, o := range got.Source {
				found := false
				for _, want := range observations {
					if want.ID == o.ID {
						found = true
						if want.Scanner != o.Scanner || !reflect.DeepEqual(want.Findings, o.Findings) && len(want.Findings) > 0 {
							t.Fatalf("selected receipt changed: %+v %+v", want, o)
						}
					}
				}
				if !found {
					t.Fatalf("unknown selected ID: %s", o.ID)
				}
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
		return conformance.ScanObservation{RunID: idstest.N(run), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: main}, ExpectedScanners: []string{"osv", "gitleaks"}, Scanner: scanner, Verdict: "pass", ObservedAt: at}
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
		{"running image not scanned", []conformance.ScanObservation{source(1, "osv", now), source(1, "gitleaks", now)}, "the image running on main has not been scanned since it was deployed"},
		{"older than three days", []conformance.ScanObservation{source(1, "osv", old), source(1, "gitleaks", old), image}, "the last scan was on " + old.Format("2 Jan") + ", more than three days ago"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTestScans(main, running, tc.observations, now)
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
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
	if err != nil || !got.Unscheduled || got.Known || got.Reason != "its packs schedule no scan" {
		t.Fatalf("unscheduled: %+v %v", got, err)
	}
	s.scanSchedules = noScanSchedule{err: errors.New("cannot read schedule")}
	if _, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0); err == nil {
		t.Fatal("read failure became no schedule")
	}
}

type noScanSchedule struct{ err error }

func (s noScanSchedule) ScansSource(context.Context, ids.ID) (bool, error) { return false, s.err }

func TestUnscannedRunningImagesUseSingularAndPlural(t *testing.T) {
	now := time.Now()
	source := conformance.ScanObservation{RunID: idstest.N(1), Subject: conformance.ScanSubject{On: "source", Commit: "main", Tree: "main"}, Scanner: "osv", ExpectedScanners: []string{"osv"}, Verdict: "pass", ObservedAt: now}
	running := []RunningImages{{DeploymentID: idstest.N(2), Deployment: "main"}, {DeploymentID: idstest.N(3), Deployment: "fly"}}
	for _, tc := range []struct {
		n    int
		want string
	}{
		{1, "the image running on main has not been scanned since it was deployed"},
		{2, "the images running on main and fly have not been scanned since they were deployed"},
	} {
		got := classifyTestScans("main", running[:tc.n], []conformance.ScanObservation{source}, now)
		if got.Known || got.Reason != tc.want {
			t.Fatalf("%d deployments: %+v", tc.n, got)
		}
	}
}

func classifyTestScans(main string, running []RunningImages, observations []conformance.ScanObservation, now time.Time) MainScans {
	heads := []conformance.ScanHead{}
	for i := range observations {
		if observations[i].ID.IsZero() {
			observations[i].ID = ids.New(now)
		}
		o := observations[i]
		heads = append(heads, conformance.ScanHead{ID: o.ID, RunID: o.RunID, Subject: o.Subject, Scanner: o.Scanner, BindingVersion: o.BindingVersion, ConfigHash: o.ConfigHash, ObservedAt: o.ObservedAt})
	}
	return latestMainScans(main, running, observations, selectScanHeads(main, running, heads), now)
}
func (s noScanSchedule) ScansSourceFrom(context.Context, ids.ID, ProcedureFacts) (bool, error) {
	return false, s.err
}

// The head selector alone decides which receipts the current view loads.
func TestCurrentScansLoadOnlySelectedReceiptCandidates(t *testing.T) {
	t.Parallel()
	s, ch, repo, _, running := automatedFixture(t)
	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	running.rows = []RunningImages{{Images: map[string]string{"web": "digest"}}}
	now := time.Now().UTC()
	observations := []conformance.ScanObservation{}
	for i := 0; i < 99; i++ {
		on, commit, scanner, run := "source", "old", "osv", idstest.N(uint64(i+100))
		if i < 2 {
			commit = main
			run = idstest.N(1)
			scanner = []string{"osv", "gitleaks"}[i]
		}
		if i == 2 {
			on = "running"
			run = idstest.N(2)
		}
		observations = append(observations, conformance.ScanObservation{ID: idstest.N(uint64(i + 1000)), RunID: run, Subject: conformance.ScanSubject{On: on, Commit: commit, Tree: map[string]string{main: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}[commit], Digest: map[string]string{"running": "digest"}[on]}, Scanner: scanner, Verdict: "pass", ObservedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	recordObservations(t, s, ch, observations)
	calls := 0
	stop := s.db.ObserveStatements("-- name: ScanObservationsByID :", func(args []any) {
		calls++
		encoded, err := args[1].(driver.Valuer).Value()
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Trim(encoded.(string), "{}")
		ids := strings.Split(got, ",")
		if len(ids) != 3 {
			t.Fatalf("loaded %d receipts, want 3 candidates: %s", len(ids), got)
		}
		for _, o := range observations[:3] {
			if !strings.Contains(got, o.ID.String()) {
				t.Errorf("missing candidate %s: %s", o.ID, got)
			}
		}
	})
	defer stop()
	if _, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("receipt reads=%d, want one", calls)
	}
}

func TestCurrentScansKeepNewestRunningRunWithoutFillingFromAnOlderRun(t *testing.T) {
	t.Parallel()
	s, ch, repo, _, running := automatedFixture(t)
	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	now := time.Now().UTC()
	dep, bundle := idstest.N(201), idstest.N(202)
	running.rows = []RunningImages{{DeploymentID: dep, BundleID: bundle, Images: map[string]string{"web": "digest"}}}
	subject := conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest"}
	current, old, olderPeer := ids.New(now), ids.New(now), ids.New(now)
	recordObservations(t, s, ch, []conformance.ScanObservation{
		{RunID: idstest.N(10), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, Scanner: "osv", Verdict: "pass", ObservedAt: now},
		{ID: olderPeer, RunID: idstest.N(11), Subject: subject, Scanner: "licenses", Verdict: "pass", ObservedAt: now.Add(-2 * time.Hour)},
		{ID: old, RunID: idstest.N(11), Subject: subject, Scanner: "trivy", Verdict: "pass", ObservedAt: now.Add(-time.Hour)},
		{ID: current, RunID: idstest.N(12), Subject: subject, Scanner: "trivy", Verdict: "error", Reason: "newest running scan failed", ObservedAt: now.Add(-time.Minute)},
	})
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
	if err != nil || got.Known || len(got.Running) != 1 || got.Running[0].ID != current || got.Reason != "newest running scan failed" {
		t.Fatalf("current scans: %+v %v", got, err)
	}
	older, err := s.LatestMainScans(t.Context(), ch.ProjectID, 10)
	if err != nil || !reflect.DeepEqual(got.Running, older.Running) || len(older.Historical) != 2 || older.Historical[0].ID != old || older.Historical[1].ID != olderPeer {
		t.Fatalf("older run changed current: %+v %v", older, err)
	}
}

func TestMainScansReuseOnlyExactTreesAndScannerBindings(t *testing.T) {
	for _, mismatch := range []string{"", "tree", "version", "config", "missing tree", "whole missing tree", "whole different tree"} {
		t.Run(mismatch, func(t *testing.T) {
			t.Parallel()
			s, ch, repo, _, _ := automatedFixture(t)
			donorCommit := gittest.Run(t, repo, "rev-parse", "HEAD")
			gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "candidate with the same tree")
			candidate := gittest.Run(t, repo, "rev-parse", "HEAD")
			tree := gittest.Run(t, repo, "rev-parse", "HEAD^{tree}")
			gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "merge with the same tree")
			main := gittest.Run(t, repo, "rev-parse", "HEAD")
			if main == candidate {
				t.Fatal("fixture needs different commits")
			}
			now := time.Now().UTC()
			donor := conformance.ScanObservation{RunID: idstest.N(501), Subject: conformance.ScanSubject{On: "source", Commit: donorCommit, Tree: tree}, Scanner: "gitleaks", BindingVersion: 1, ConfigHash: "config", ExpectedScanners: []string{"osv", "gitleaks"}, Verdict: "pass", ObservedAt: now.Add(-time.Hour)}
			if mismatch == "tree" {
				donor.Subject.Tree = "different"
			}
			if mismatch == "missing tree" {
				donor.Subject.Tree = ""
			}
			if mismatch == "version" {
				donor.BindingVersion++
			}
			if mismatch == "config" {
				donor.ConfigHash = "different"
			}
			whole := donor
			whole.RunID, whole.Scanner, whole.Subject, whole.ObservedAt = idstest.N(502), "osv", conformance.ScanSubject{On: "source", Commit: candidate, Tree: tree}, now
			if mismatch == "whole missing tree" {
				whole.Subject.Tree, donor.Subject.Tree = "", ""
			}
			if mismatch == "whole different tree" {
				whole.Subject.Tree, donor.Subject.Tree = "other", "other"
			}
			history := whole
			history.Scanner, history.BindingVersion, history.ConfigHash, history.Subject.History = "gitleaks", 1, "config", "base..candidate"
			retry := whole
			retry.ObservedAt, retry.Verdict = now.Add(-time.Minute), "error"
			recordObservations(t, s, ch, []conformance.ScanObservation{donor, whole, history, retry})
			got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
			if err != nil || got.Known != (mismatch == "") {
				t.Fatalf("scans: %+v %v", got, err)
			}
			if strings.HasPrefix(mismatch, "whole ") {
				if len(got.Source) != 0 {
					t.Fatalf("different or absent tree answered main: %+v", got)
				}
				return
			}
			if len(got.Source) == 0 || got.Source[0].Subject.Commit != candidate {
				t.Fatalf("landing tree did not answer merge: %+v", got)
			}
		})
	}
}

func TestRunningScansReuseNewestRunOfEachExactDigest(t *testing.T) {
	t.Parallel()
	s, ch, repo, _, running := automatedFixture(t)
	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	now := time.Now().UTC()
	running.rows = []RunningImages{{DeploymentID: idstest.N(601), Deployment: "production", BundleID: idstest.N(602), Images: map[string]string{"web": "digest-a", "init/db": "digest-b"}}}
	recordObservations(t, s, ch, []conformance.ScanObservation{
		{RunID: idstest.N(603), Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", "HEAD^{tree}")}, Scanner: "osv", Verdict: "pass", ObservedAt: now},
		{RunID: idstest.N(604), Subject: conformance.ScanSubject{On: "bundle", BundleID: idstest.N(605), Part: "other", Digest: "digest-a"}, Scanner: "trivy", Verdict: "pass", ObservedAt: now.Add(-time.Hour)},
		{RunID: idstest.N(606), Subject: conformance.ScanSubject{On: "running", Digest: "digest-b"}, Scanner: "trivy", Verdict: "pass", ObservedAt: now},
	})
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID, 0)
	if err != nil || !got.Known || len(got.Running) != 2 {
		t.Fatalf("reused bundle: %+v %v", got, err)
	}
	for _, o := range got.Running {
		if o.Subject.Deployment != "production" || o.Subject.DeploymentID != running.rows[0].DeploymentID {
			t.Fatalf("deployment label: %+v", o)
		}
	}
	running.rows[0].Images["init/db"] = "different"
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID, 0)
	if err != nil || got.Known {
		t.Fatalf("different digest: %+v %v", got, err)
	}
	running.rows = nil
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID, 0)
	if err != nil || !got.Known || len(got.Running) != 0 {
		t.Fatalf("source only: %+v %v", got, err)
	}
}
