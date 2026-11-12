package workflow

import (
	"encoding/json"
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
