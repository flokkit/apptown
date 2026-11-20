package workflow

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/status"
)

func TestStepLabelsPreserveUnreadEvidenceExceptionsAndEndedRuns(t *testing.T) {
	for _, tc := range []struct {
		s    Step
		want status.DisplayStatus
	}{
		{Step{Status: StatusPass}, status.DisplayPassed},
		{Step{Status: StatusFail}, status.DisplayFailed},
		{Step{Status: StatusError}, status.DisplayUnknown},
		{Step{Status: StatusPass, CheckReading: &checkReading{Unreadable: "missing"}}, status.DisplayUnknown},
		{Step{Status: StatusFail, OverrideBy: "alice"}, status.DisplayExcepted},
		{Step{Status: StatusRunning}, status.DisplayWorking},
		{Step{Status: StatusRunning, runEnded: true}, status.DisplayStopped},
		{Step{Status: StatusPending, runEnded: true}, status.DisplayStopped},
		{Step{Status: StatusNotApplicable}, status.DisplayNotApplicable},
	} {
		if got := tc.s.displayLabel(); got != tc.want {
			t.Errorf("%+v: got %s want %s", tc.s, got, tc.want)
		}
	}
}

func TestRunLabelsDescribeWorkRatherThanRuntime(t *testing.T) {
	t.Parallel()
	for state, want := range map[RunState]status.DisplayStatus{RunRunning: status.DisplayWorking, RunDone: status.DisplayFinished, RunAbandoned: status.DisplayAbandoned, 0: status.DisplayUnknown} {
		if got := state.display(); got != want {
			t.Errorf("%v: got %v, want %v", state, got, want)
		}
	}
}

func TestCheckReadingLabelsKeepUncertaintyAndExceptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want status.DisplayStatus
	}{
		{`{"checks":[{"verdict":"pass"}]}`, status.DisplayPassed},
		{`{"checks":[{"verdict":"fail"}]}`, status.DisplayFailed},
		{`{"checks":[{"verdict":"fail","preexisting":true}]}`, status.DisplayUnknown},
		{`{"checks":[{"verdict":"fail","undecided":true}]}`, status.DisplayUnknown},
		{`{"checks":[{"verdict":"excepted"}]}`, status.DisplayExcepted},
		{`{"checks":[{"verdict":"error"}]}`, status.DisplayUnknown},
		{`{"checks":[{"verdict":"cannot-assess"}]}`, status.DisplayUnknown},
		{`{"checks":[{"verdict":"unclear"}]}`, status.DisplayUnknown},
		{`{"checks":[{"verdict":"skipped"}]}`, status.DisplayStopped},
		{`{"checks":[{"verdict":"not-applicable"}]}`, status.DisplayNotApplicable},
	} {
		r := readingOf([]byte(tc.raw))
		if r.Unreadable != "" || len(r.Checks) != 1 || r.Checks[0].StatusLabel != tc.want {
			t.Errorf("%s: %+v", tc.raw, r)
		}
	}
	if status.MergeDisplay(true) != status.DisplayLanded || status.MergeDisplay(false) != status.DisplayNone {
		t.Fatal("merge evidence label")
	}
}

// Reading the web response must preserve both the checked facts and their
// display label, rather than using only the embedded stored-evidence decoder.
func TestCheckReadingRoundTripPreservesDisplayLabelsAndFacts(t *testing.T) {
	t.Parallel()
	reading := readingOf([]byte(`{"checks":[{"check":"tests","verdict":"pass","files":["main.go"]}]}`))
	body, err := json.Marshal(reading)
	if err != nil {
		t.Fatal(err)
	}
	var got checkReading
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Checks) != 1 {
		t.Fatalf("checks: %+v", got)
	}
	check := got.Checks[0]
	if check.Check != "tests" || check.Verdict != VerdictPass || check.StatusLabel != status.DisplayPassed || len(check.Files) != 1 || check.Files[0] != "main.go" {
		t.Fatalf("display response lost checked facts or label: %+v", check)
	}
}

func TestGatePassDoesNotHideUnmetRules(t *testing.T) {
	s := Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Check: "keep-data", Verdict: VerdictFail, Preexisting: true}}}}}
	if got := s.displayLabel(); got != status.DisplayFailed {
		t.Fatalf("unmet rule: %s", got)
	}
	if s.Status != StatusPass {
		t.Fatal("display changed gate outcome")
	}
}

func TestPassingStepOnlyDisplaysUnmetBlockingRules(t *testing.T) {
	nonGating, gating := false, true
	for _, tc := range []struct {
		name, kind string
		gates      *bool
		want       status.DisplayStatus
	}{
		{"advisory model rule", "guidance/conformance", &nonGating, status.DisplayPassed},
		{"blocking rule", "guidance/conformance", &gating, status.DisplayFailed},
		{"default blocking rule", "guidance/conformance", nil, status.DisplayFailed},
		{"script baseline", "script", nil, status.DisplayPassed},
		{"scan baseline", "scan", nil, status.DisplayPassed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Step{Type: tc.kind, Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Check: "rule", Verdict: VerdictFail, Gates: tc.gates, Preexisting: true}}}}}
			if got := s.displayLabel(); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			if s.Status != StatusPass {
				t.Fatal("display changed gate outcome")
			}
		})
	}
}

// A ready decision and a decision behind unfinished checks have different words.
func TestPendingApprovalLabelUsesReadiness(t *testing.T) {
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "check", Uses: "script"}, {Name: "accept", Uses: "approval"}}}}})
	service := newService(q.db, allowProject(run.ProjectID), Runtime{})
	read := func(want status.DisplayStatus) {
		t.Helper()
		snapshot, err := service.Snapshot(t.Context(), run.ID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(snapshot.Steps[1])
		if err != nil || !strings.Contains(string(body), `"statusLabel":"`+want.String()+`"`) {
			t.Fatalf("approval label: %s %v", body, err)
		}
	}
	read(status.DisplayWaitingForChecks)
	steps, err := service.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts := newStepAttempts(q.db, func(string) bool { return true })
	attempt, err := attempts.start(t.Context(), steps[0], "script", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempts.finish(t.Context(), attempt, StepOutcome{Status: StatusPass}); err != nil {
		t.Fatal(err)
	}
	read(status.DisplayWaitingForApproval)
}

func TestProgressPreservesSettledResultsAndUncertainty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		step Step
		want string
	}{
		{"not applicable", Step{Status: StatusNotApplicable}, "not_applicable"},
		{"skipped", Step{Status: StatusSkipped}, "skipped"},
		{"held check", Step{Type: "script", Status: StatusFail}, "failed"},
		{"needs a person", Step{Status: StatusNeedsHuman}, "failed"},
		{"observation unreadable", Step{Observation: true, Type: "scan", Status: StatusFail, CheckReading: &checkReading{Unreadable: "missing"}}, "unknown"},
		{"held build", Step{Type: "build", Status: StatusFail}, "failed"},
		{"build error", Step{Type: "build", Status: StatusError}, "unknown"},
		{"observation findings", Step{Observation: true, Type: "scan", Status: StatusFail}, "done"},
		{"observation outage", Step{Observation: true, Type: "scan", Status: StatusError}, "unknown"},
		{"undecided", Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Undecided: []string{"Performance"}}}, "unknown"},
		{"blocking receipt", Step{Type: "script", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Verdict: VerdictFail}}}}}, "failed"},
		{"non-gating passed blocking receipt", Step{Observation: true, Type: "script", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Verdict: VerdictFail}}}}}, "failed"},
		{"check errors", Step{Type: "script", Status: StatusPass, CheckReading: &checkReading{CheckErrors: 1}}, "unknown"},
		{"unreadable", Step{Type: "scan", Status: StatusPass, CheckReading: &checkReading{Unreadable: "missing"}}, "unknown"},
		{"test counts missing", Step{Type: "tests", Status: StatusPass}, "unknown"},
		{"skipped tests", Step{Type: "tests", Status: StatusPass, Evidence: []byte(`{"testCounts":{"skipped":1}}`)}, "unknown"},
		{"override", Step{Status: StatusFail, OverrideBy: "sam"}, "unknown"},
		{"ended", Step{Status: StatusRunning, runEnded: true}, "unknown"},
		{"running", Step{Status: StatusRunning}, "running"},
		{"pending", Step{Status: StatusPending}, "waiting"},
		{"passed", Step{Status: StatusPass}, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.step.CheckReading != nil {
				for i := range tc.step.CheckReading.Checks {
					tc.step.CheckReading.Checks[i].StatusLabel = tc.step.CheckReading.Checks[i].CheckReceipt.display()
				}
			}
			raw, err := json.Marshal(tc.step)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct{ Progress string }
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Progress != tc.want {
				t.Fatalf("JSON progress %s want %s", wire.Progress, tc.want)
			}
			if got := tc.step.progress(); got != tc.want {
				t.Fatalf("progress=%s want %s", got, tc.want)
			}
		})
	}
}

// A passed step whose checks could not all be assessed reads unknown, gating
// or not: live's Standards step read "passed" over five AI checks that never
// reached a model. A non-gating check that failed is still an answer (a
// note), and the gate's outcome is unchanged.
//
// Kills: a non-gating cannot-assess counted as passed.
func TestAPassedStepWithAnUnassessedCheckIsNotPassed(t *testing.T) {
	t.Parallel()
	advisory := false
	unassessed := &Step{Type: "guidance/conformance", Name: "guidance-conformance", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{
		{CheckReceipt: CheckReceipt{Check: "shell", Verdict: VerdictPass}},
		{CheckReceipt: CheckReceipt{Check: "rubric", Verdict: VerdictCannotAssess, Gates: &advisory, Detail: "check agent exited with status 1"}},
	}}}
	if got := unassessed.displayLabel(); got != status.DisplayUnknown {
		t.Fatalf("label %s", got)
	}
	if got := unassessed.progress(); got != "unknown" {
		t.Fatalf("progress %s", got)
	}
	for _, observation := range []bool{false, true} {
		unassessed.Observation = observation
		if got := summarizeChecks([]*Step{{Type: "script", Status: StatusPass}, unassessed}); got.Tone != "unknown" || got.Text != "Checks not all known" {
			t.Fatalf("summary (observation %v) %+v", observation, got)
		}
	}
	unassessed.Observation = false
	if unassessed.Status != StatusPass || unassessed.checkAttention() {
		t.Fatal("display changed the gate outcome or raised attention")
	}
	answered := &Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Check: "rubric", Verdict: VerdictFail, Gates: &advisory}}}}}
	if got := answered.displayLabel(); got != status.DisplayPassed {
		t.Fatalf("an answered advisory check: %s", got)
	}
}
