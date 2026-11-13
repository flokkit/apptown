package workflow

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

// Execution success never overrides failed, unreadable, or excepted evidence.
func TestCheckVerdictOwnsBothSummaryAndStepAttention(t *testing.T) {
	t.Parallel()
	advisory := false
	for _, tc := range []struct {
		name      string
		step      Step
		tone      string
		attention bool
	}{
		{"pass", Step{Type: "script", Status: StatusPass}, "ok", false},
		{"blocking failure in passed step", Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Verdict: VerdictFail}}}}}, "bad", true},
		{"advisory failure", Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Verdict: VerdictFail, Gates: &advisory}}}}}, "ok", false},
		{"unreadable", Step{Type: "scan", Status: StatusPass, CheckReading: &checkReading{Unreadable: "missing"}}, "unknown", true},
		{"undecided", Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Undecided: []string{"Performance"}}}, "unknown", true},
		{"override", Step{Type: "script", Status: StatusPass, OverrideBy: "sam"}, "unknown", true},
		{"test counts unknown", Step{Type: "tests", Status: StatusPass, Evidence: json.RawMessage(`{}`)}, "unknown", true},
		{"running", Step{Type: "script", Status: StatusRunning}, "neutral", true},
		{"ended unfinished work", Step{Type: "script", Status: StatusRunning, runEnded: true}, "unknown", true},
		{"not applicable", Step{Type: "scan", Status: StatusNotApplicable}, "ok", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := summarizeChecks([]*Step{&tc.step})
			if tc.step.CheckReading != nil {
				for i := range tc.step.CheckReading.Checks {
					tc.step.CheckReading.Checks[i].StatusLabel = tc.step.CheckReading.Checks[i].CheckReceipt.display()
				}
			}
			raw, err := json.Marshal(tc.step)
			if err != nil {
				t.Fatal(err)
			}
			var step struct{ IsCheck, CheckAttention bool }
			if err := json.Unmarshal(raw, &step); err != nil {
				t.Fatal(err)
			}
			if summary.Tone != tc.tone || !step.IsCheck || step.CheckAttention != tc.attention {
				t.Fatalf("summary %+v / step %s", summary, raw)
			}
		})
	}
}

func TestCheckDefinitionAndVerdictKeepWorkSeparate(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"tests", "scan", "script", "guidance/conformance", "agent/review", "scm/merged"} {
		if !isCheck(kind) {
			t.Errorf("%s is a check", kind)
		}
	}
	for _, kind := range []string{"approval", "agent/pi", "preview", "workflow/call", "build", "reconcile", "artifact/require", "adr/require"} {
		step := Step{Type: kind, Status: StatusFail}
		if isCheck(kind) || summarizeChecks([]*Step{&step}).Started {
			t.Errorf("%s is work, not a check", kind)
		}
	}
	failed := Step{Type: "script", Status: StatusFail}
	running := Step{Type: "scan", Status: StatusRunning}
	unknown := Step{Type: "tests", Status: StatusPass, Evidence: json.RawMessage(`{}`)}
	if got := summarizeChecks([]*Step{&running, &unknown, &failed}); got.Tone != "bad" {
		t.Fatal(got)
	}
	if got := summarizeChecks([]*Step{&running, &unknown}); got.Tone != "unknown" {
		t.Fatal(got)
	}
	pending := Step{Type: "script", Status: StatusPending}
	skipped := Step{Type: "script", Status: StatusSkipped}
	if got := summarizeChecks([]*Step{&pending, &skipped}); got.Started {
		t.Fatal(got)
	}
}

// A landing's own successful checks cannot hide an unresolved working check.
func TestChangeVerdictIncludesTheWorkingRunCarriedByLanding(t *testing.T) {
	t.Parallel()
	q, work, _ := activityFixture(t)
	finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: work.ID, Stage: "finish", Name: "privacy", Type: "script"}, StatusFail)
	landing := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"landing","stages":[{"name":"finish"}]}`)})
	finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing.ID, Stage: "finish", Name: "land-check", Type: "script"}, StatusPass)
	for _, run := range []*Run{work, landing} {
		for _, name := range []string{"security", "license"} {
			finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: name, Type: "scan"}, StatusNotApplicable)
		}
	}
	s := newService(q.db, allowProject(work.ProjectID), Runtime{})
	latest, err := s.Snapshot(t.Context(), landing.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Checks.Tone != "ok" {
		t.Fatal(latest.Checks)
	}
	summary, err := s.ChangeChecks(t.Context(), latest)
	if err != nil || summary.Tone != "bad" || summary.Text != "Privacy failed · 1 passed, 2 skipped: Security scan, Licence scan" {
		t.Fatalf("change verdict %+v %v", summary, err)
	}
}

// Omitted checks are named without turning a conditional omission into a pass.
func TestChecksSummaryCountsAndNamesSkippedChecks(t *testing.T) {
	t.Parallel()
	pass := &Step{Type: "script", Status: StatusPass}
	na := &Step{Type: "scan", Name: "osv", Status: StatusNotApplicable}
	advisory := false
	noted := &Step{Type: "guidance/conformance", Status: StatusPass, CheckReading: &checkReading{Checks: []checkReceiptView{{CheckReceipt: CheckReceipt{Verdict: VerdictFail, Gates: &advisory}}}}}
	skipped := &Step{Type: "script", Name: "optional check", Status: StatusSkipped}
	for _, tc := range []struct {
		steps      []*Step
		tone, text string
	}{
		{[]*Step{{Type: "scan", Name: "security", Status: StatusNotApplicable}, {Type: "scan", Name: "license", Status: StatusSkipped}, {Type: "scan", Name: "security", Status: StatusSkipped}, {Type: "scan", Name: "license", Status: StatusNotApplicable}}, "unknown", "Checks not all known · 0 passed, 2 skipped: Security scan, Licence scan"},
		{[]*Step{pass, na}, "ok", "1 passed, 1 skipped: Dependency scan"},
		{[]*Step{noted, na}, "ok", "1 passed, 1 skipped: Dependency scan · 1 note"},
		{[]*Step{pass}, "ok", "All checks passed"},
		{[]*Step{pass, skipped}, "unknown", "Checks not all known · 1 passed, 1 skipped: Optional check"},
		{[]*Step{na}, "ok", "0 passed, 1 skipped: Dependency scan"},
		{[]*Step{skipped}, "unknown", "Checks not all known · 0 passed, 1 skipped: Optional check"},
	} {
		got := summarizeChecks(tc.steps)
		if got.Tone != tc.tone || got.Text != tc.text {
			t.Fatalf("got %+v, want %s / %s", got, tc.tone, tc.text)
		}
	}
}

func TestStepRegistrationOwnsSummaryAttentionAndExecutor(t *testing.T) {
	schema.StepTypes["synthetic/check"] = schema.StepTypes["tests"]
	defer delete(schema.StepTypes, "synthetic/check")
	for _, kind := range []string{"synthetic/check", "scm/merged"} {
		step := Step{Type: kind, Status: StatusFail}
		if !summarizeChecks([]*Step{&step}).Started || !step.checkAttention() {
			t.Errorf("%s missing from summary or attention", kind)
		}
	}
	if !isCheckKind("synthetic/check") || !carriesCheckEvidence("synthetic/check") {
		t.Fatal("registered check did not reach executor/evidence")
	}
	if !carriesCheckEvidence("script") || isCheckKind("script") {
		t.Fatal("script lost placement evidence or became a check executor type")
	}
	if isCheckKind("scm/merged") {
		t.Fatal("internal merge gate became a check executor type")
	}
}
