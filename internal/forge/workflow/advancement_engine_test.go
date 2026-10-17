package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// No App, database, Git checkout, or execution runtime is needed to exercise
// advancement. The trace records externally observable operations and ordering.
type workflowTestRecords struct {
	run          *Run
	steps        []*Step
	checks       []Check
	trace        []string
	readErr      error
	writeErr     error
	transitionBy string
}

func (s *workflowTestRecords) runByID(context.Context, ids.ID) (*Run, error) { return s.run, s.readErr }
func (s *workflowTestRecords) stepsForRunStage(context.Context, ids.ID, string) ([]*Step, error) {
	s.trace = append(s.trace, "read checks")
	return s.steps, nil
}
func (s *workflowTestRecords) transition(_ context.Context, _ *Run, next, who string) ([]ReadyStep, error) {
	s.trace = append(s.trace, "commit stage")
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	s.transitionBy = who
	s.run.CurrentStage = next
	return []ReadyStep{{ID: idstest.N(42), Type: "script"}}, nil
}
func (s *workflowTestRecords) complete(context.Context, *Run) error {
	s.trace = append(s.trace, "commit done")
	if s.writeErr != nil {
		return s.writeErr
	}
	s.run.Status = RunDone
	return nil
}

func testWorkflowEngine(t *testing.T, spec schema.Spec, stage string) (advanceEngine, *workflowTestRecords) {
	t.Helper()
	s := &workflowTestRecords{run: &Run{ID: idstest.N(7), Status: RunRunning, CurrentStage: stage, DefinitionJSON: mustJSON(spec)}}
	w := advanceEngine{
		records:    s,
		refreshLaw: func(context.Context, *Run) error { s.trace = append(s.trace, "refresh law"); return nil },
		assert: func(_ context.Context, id ids.ID) (*Step, error) {
			s.trace = append(s.trace, "assert")
			for _, step := range s.steps {
				if step.ID == id {
					next := *step
					next.Status = StatusPass
					return &next, nil
				}
			}
			return nil, errors.New("missing assertion")
		},
		merge: func(_ context.Context, run *Run) (MergeResult, error) {
			phase := "before deploy"
			if run.CurrentStage == spec.Stages[len(spec.Stages)-1].Name {
				phase = "at completion"
			}
			s.trace = append(s.trace, "merge "+phase)
			return MergeResult{Allowed: true}, nil
		},

		note: func(_ context.Context, _ ids.ID, kind string, _ advanceBlockedReceipt) {
			s.trace = append(s.trace, kind)
		},
	}
	return w, s
}

func TestWorkflowEngineGatesBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, status := range []StepState{StatusPending, StatusRunning, StatusFail, StatusError, StatusNeedsHuman, StatusPass, StatusSkipped} {
		t.Run(status.String(), func(t *testing.T) {
			w, s := testWorkflowEngine(t, schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "check"}, {Name: "deploy"}}}, "check")
			s.steps = []*Step{{Name: "review", Status: status}}
			res, err := w.advance(context.Background(), idstest.N(7), "alice")
			if err != nil {
				t.Fatal(err)
			}
			passes := status == StatusPass || status == StatusSkipped
			if res.Advanced != passes {
				t.Fatalf("advance = %+v", res)
			}
			want := []string{"refresh law", "read checks", "advance.blocked"}
			if passes {
				want = []string{"refresh law", "read checks", "merge before deploy", "commit stage"}
				if s.transitionBy != "alice" || res.Stage != "deploy" || len(res.Ready) != 1 || res.Ready[0].ID != idstest.N(42) || res.Ready[0].Type != "script" {
					t.Fatalf("transition: %+v, by %s", res, s.transitionBy)
				}
			} else if len(res.Blocking) != 1 || res.Blocking[0] != s.steps[0] {
				t.Fatalf("missing blocking check: %+v", res)
			}
			if !reflect.DeepEqual(s.trace, want) {
				t.Fatalf("effects = %v, want %v", s.trace, want)
			}
		})
	}
}

func TestWorkflowEngineRefreshesAssertionsAndHonorsGateSubset(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "evidence", Uses: "artifact/require"}, {Name: "approval", Uses: "approval"}, {Name: "optional", Uses: "script"}}, Gate: &schema.Gate{Require: []string{"evidence", "approval"}}}}}
	w, s := testWorkflowEngine(t, spec, "check")
	s.steps = []*Step{
		{Name: "evidence", Type: "artifact/require", Status: StatusPending},
		{Name: "approval", Type: "approval", Status: StatusPass},
		{Name: "optional", Type: "script", Status: StatusFail},
	}
	res, err := w.advance(context.Background(), idstest.N(7), "alice")
	if err != nil || !res.Advanced || res.RunStatus != RunDone {
		t.Fatalf("advance = %+v, %v", res, err)
	}
	if !reflect.DeepEqual(res.Required, []string{"approval", "evidence"}) {
		t.Fatalf("required = %v", res.Required)
	}
	want := []string{"refresh law", "read checks", "assert", "commit done"}
	if !reflect.DeepEqual(s.trace, want) {
		t.Fatalf("effects = %v, want %v", s.trace, want)
	}
}

func TestWorkflowEngineMergeBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, role, stage, merge string
		parent                   ids.ID
	}{
		{"working completion", "working", "deploy", "", ids.Nil},
		{"old definition", "", "deploy", "", ids.Nil},
		{"child completion", "landing", "deploy", "", idstest.N(99)},
		{"child deployment", "landing", "check", "", idstest.N(99)},
		{"landing deployment", "landing", "check", "merge before deploy", ids.Nil},
		{"landing completion", "landing", "deploy", "merge at completion", ids.Nil},
		{"legacy completion", "fused", "deploy", "merge at completion", ids.Nil},
		// A record run keeps records, not commits: nothing merges.
		{"record completion", "record", "deploy", "", ids.Nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, s := testWorkflowEngine(t, schema.Spec{Role: tc.role, Stages: []schema.Stage{{Name: "check"}, {Name: "deploy"}}}, tc.stage)
			s.run.ParentRunID = tc.parent
			res, err := w.advance(context.Background(), idstest.N(7), "system")
			if err != nil || !res.Advanced {
				t.Fatalf("advance = %+v, %v", res, err)
			}
			want := []string{"refresh law", "read checks"}
			if tc.merge != "" {
				want = append(want, tc.merge)
			}
			if tc.stage == "deploy" {
				want = append(want, "commit done")
			} else {
				want = append(want, "commit stage")
			}
			if !reflect.DeepEqual(s.trace, want) {
				t.Fatalf("effects = %v, want %v", s.trace, want)
			}
		})
	}
}

func TestWorkflowEngineMergeRefusalCannotAdvance(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"check", "deploy"} {
		for _, block := range []*Step{nil, {Name: "merge", Status: StatusNeedsHuman}} {
			w, s := testWorkflowEngine(t, schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "check"}, {Name: "deploy"}}}, stage)
			w.merge = func(context.Context, *Run) (MergeResult, error) {
				return MergeResult{Blocking: block}, nil
			}
			res, err := w.advance(context.Background(), idstest.N(7), "alice")
			if err != nil || res.Advanced || s.run.Status != RunRunning || s.run.CurrentStage != stage {
				t.Fatalf("advance = %+v, %v", res, err)
			}
			if block != nil && (len(res.Blocking) != 1 || res.Blocking[0] != block) {
				t.Fatalf("missing merge block: %+v", res)
			}
			if !reflect.DeepEqual(s.trace, []string{"refresh law", "read checks"}) {
				t.Fatalf("effects after merge refusal: %v", s.trace)
			}
		}
	}
}

func TestWorkflowEngineStopsOnFailedWrites(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"check", "deploy"} {
		w, s := testWorkflowEngine(t, schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "check"}, {Name: "deploy"}}}, stage)
		s.writeErr = errors.New("database unavailable")
		res, err := w.advance(context.Background(), idstest.N(7), "alice")
		if !errors.Is(err, s.writeErr) || res != nil {
			t.Fatalf("advance = %+v, %v", res, err)
		}
		if s.run.Status != RunRunning || s.run.CurrentStage != stage {
			t.Fatalf("state changed after failed write: %+v", s.run)
		}
		want := []string{"refresh law", "read checks", "commit stage"}
		if stage == "deploy" {
			want[2] = "commit done"
		}
		if !reflect.DeepEqual(s.trace, want) {
			t.Fatalf("effects = %v, want %v", s.trace, want)
		}
	}
}

func TestWorkflowEngineInvalidOrStoppedRunDoesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"read error", "corrupt definition", "unknown stage", "abandoned"} {
		t.Run(tc, func(t *testing.T) {
			w, s := testWorkflowEngine(t, schema.Spec{Stages: []schema.Stage{{Name: "check"}}}, "check")
			wantErr := true
			switch tc {
			case "read error":
				s.readErr = errors.New("read failed")
			case "corrupt definition":
				s.run.DefinitionJSON = "{"
			case "unknown stage":
				s.run.CurrentStage = "missing"
			default:
				s.run.Status = RunAbandoned
				wantErr = false
			}
			res, err := w.advance(context.Background(), idstest.N(7), "alice")
			if (err != nil) != wantErr {
				t.Fatalf("advance = %+v, %v", res, err)
			}
			if len(s.trace) != 0 {
				t.Fatalf("unexpected effects: %v", s.trace)
			}
		})
	}
}

func TestWorkflowEnginePropagatesCheckAndMergeErrors(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"assertion", "merge", "policy"} {
		t.Run(operation, func(t *testing.T) {
			w, s := testWorkflowEngine(t, schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "check"}}}, "check")
			failure := errors.New("cannot verify")
			if operation == "policy" {
				w.refreshLaw = func(context.Context, *Run) error { s.trace = append(s.trace, "refresh law"); return failure }
			} else if operation == "assertion" {
				s.steps = []*Step{{Name: "evidence", Type: "artifact/require", Status: StatusPending}}
				w.assert = func(context.Context, ids.ID) (*Step, error) { return nil, failure }
			} else {
				w.merge = func(context.Context, *Run) (MergeResult, error) { return MergeResult{}, failure }
			}
			res, err := w.advance(context.Background(), idstest.N(7), "alice")
			if !errors.Is(err, failure) || res != nil {
				t.Fatalf("advance = %+v, %v", res, err)
			}
			want := []string{"refresh law", "read checks"}
			if operation == "policy" {
				want = []string{"refresh law"}
			}
			if !reflect.DeepEqual(s.trace, want) {
				t.Fatalf("unexpected effects: %v", s.trace)
			}
		})
	}
}

func TestWorkflowEngineRequiresEveryDeclaredReceipt(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		stage := schema.Stage{Name: "check", Steps: []schema.Step{{Name: "review", Uses: "approval"}}}
		if explicit {
			stage.Gate = &schema.Gate{Require: []string{"review"}}
		}
		w, s := testWorkflowEngine(t, schema.Spec{Role: "landing", Stages: []schema.Stage{stage, {Name: "deploy"}}}, "check")
		res, err := w.advance(context.Background(), idstest.N(7), "alice")
		if err != nil {
			t.Fatal(err)
		}
		if res.Advanced {
			t.Errorf("explicit=%v: missing review allowed effects: %v", explicit, s.trace)
		}
	}
}

func TestWorkflowEngineExpandedScanCannotDisappearFromGate(t *testing.T) {
	t.Parallel()
	stage := schema.Stage{Name: "check", Steps: []schema.Step{{Name: "security", Uses: "scan", With: map[string]any{"on": "source"}}}, Gate: &schema.Gate{Require: []string{"security"}}}
	w, s := testWorkflowEngine(t, schema.Spec{Role: "landing", Stages: []schema.Stage{stage, {Name: "deploy"}}}, "check")
	s.steps = []*Step{{Name: "security:trivy", Type: "scan", Status: StatusPass}, {Name: "security:semgrep", Type: "scan", Status: StatusFail}}
	s.checks = []Check{{Declaration: "security", Name: "security:trivy", Type: "scan"}, {Declaration: "security", Name: "security:semgrep", Type: "scan"}}
	res, err := w.advance(context.Background(), idstest.N(7), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Advanced {
		t.Fatalf("failed expanded scanner allowed effects: %v", s.trace)
	}
}

func (s *workflowTestRecords) stageChecks(context.Context, ids.ID, string) ([]Check, error) {
	return s.checks, nil
}

func (s *workflowTestRecords) retryCompletion(context.Context, *Run) (bool, error) {
	s.trace = append(s.trace, "retry completion")
	return true, s.writeErr
}

func TestWorkflowEngineCompletedAdvanceOnlyRetriesEffects(t *testing.T) {
	t.Parallel()
	w, s := testWorkflowEngine(t, schema.Spec{}, "finish")
	s.run.Status = RunDone
	result, err := w.advance(context.Background(), idstest.N(7), "alice")
	if err != nil || result.Advanced || result.RunStatus != RunDone {
		t.Fatalf("advance: %+v %v", result, err)
	}
	if !reflect.DeepEqual(s.trace, []string{"retry completion"}) {
		t.Fatalf("effects: %v", s.trace)
	}
}

func mustJSON(in any) string {
	body, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	return string(body)
}

// A run whose producing step declared an empty result a complete answer and
// found nothing to change asks to be closed instead of asking anyone to
// accept an empty change, once an independent review of its report passed
// with nothing left for a person; anything else holding the stage keeps it
// waiting for a person. Whether the change is still empty is read at the
// closing act (TestARunWithNothingToChangeEndsWithoutAsking).
func TestWorkflowEngineEndsARunWithNothingToChange(t *testing.T) {
	t.Parallel()
	nothing := json.RawMessage(`{"nothingToChange":true,"reason":"nothing to change: the update step found nothing to change"}`)
	producer := json.RawMessage(`{"output":{"kind":"uplift-report"}}`)
	of := json.RawMessage(`{"of":"uplift-report"}`)
	update := func(ev json.RawMessage) *Step {
		return &Step{Name: "update", Type: "agent/pi", Status: StatusPass, Config: producer, Evidence: ev}
	}
	review := func(status StepState, ev string) *Step {
		return &Step{Name: "review", Type: "agent/review", Status: status, Config: of, Evidence: json.RawMessage(ev)}
	}
	accept := func(status StepState) *Step { return &Step{Name: "accept", Type: "approval", Status: status} }
	for _, tc := range []struct {
		name  string
		steps []*Step
		ends  bool
	}{
		{"reviewed clean, only the approval waits", []*Step{update(nothing), review(StatusPass, `{"findings":[]}`), accept(StatusPending)}, true},
		{"the review left something for a person", []*Step{update(nothing), review(StatusPass, `{"findings":["react 19 deferred: a major"]}`), accept(StatusPending)}, false},
		{"another review left something for a person", []*Step{update(nothing), review(StatusPass, `{"findings":[]}`), {Name: "review-security", Type: "agent/review", Status: StatusPass, Config: of, Evidence: json.RawMessage(`{"findings":["react 19 deferred: a major"]}`)}, accept(StatusPending)}, false},
		{"another review left something for a person, first", []*Step{update(nothing), {Name: "review-security", Type: "agent/review", Status: StatusPass, Config: of, Evidence: json.RawMessage(`{"findings":["react 19 deferred: a major"]}`)}, review(StatusPass, `{"findings":[]}`), accept(StatusPending)}, false},
		{"a review's evidence cannot be read", []*Step{update(nothing), review(StatusPass, `{"findings":[]}`), {Name: "review-security", Type: "agent/review", Status: StatusPass, Config: of, Evidence: json.RawMessage(`not json`)}, accept(StatusPending)}, false},
		{"every review clean", []*Step{update(nothing), review(StatusPass, `{"findings":[]}`), {Name: "review-security", Type: "agent/review", Status: StatusPass, Config: of, Evidence: json.RawMessage(`{"findings":[]}`)}, accept(StatusPending)}, true},
		{"nobody reviewed the report", []*Step{update(nothing), accept(StatusPending)}, false},
		{"a failed scan waits for a person", []*Step{update(nothing), review(StatusPass, `{}`), {Name: "scan", Type: "scan", Status: StatusFail}, accept(StatusPending)}, false},
		{"a failed step outside an explicit gate still waits", []*Step{update(nothing), review(StatusPass, `{}`), {Name: "scan", Type: "scan", Status: StatusFail}, accept(StatusPending)}, false},
		{"a running review is waited for", []*Step{update(nothing), review(StatusRunning, `{}`), accept(StatusPending)}, false},
		{"a rejected approval is a decision", []*Step{update(nothing), review(StatusPass, `{}`), accept(StatusFail)}, false},
		{"a step that changed something reaches its approval", []*Step{update(json.RawMessage(`{}`)), review(StatusPass, `{}`), accept(StatusPending)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := schema.Stage{Name: "uplift", Gate: &schema.Gate{Require: []string{"accept"}}}
			if !strings.Contains(tc.name, "explicit gate") {
				stage.Gate = nil
			}
			for _, s := range tc.steps {
				stage.Steps = append(stage.Steps, schema.Step{Name: s.Name, Uses: s.Type})
			}
			w, s := testWorkflowEngine(t, schema.Spec{Stages: []schema.Stage{stage}}, "uplift")
			s.run.ChangeID = idstest.N(9)
			s.steps = tc.steps
			w.endNothing = func(context.Context, *Run) error {
				s.trace = append(s.trace, "close change")
				return nil
			}
			res, err := w.advance(context.Background(), idstest.N(7), "system")
			if err != nil {
				t.Fatal(err)
			}
			closed := slices.Contains(s.trace, "close change")
			if tc.ends != closed || tc.ends != (res.RunStatus == RunAbandoned) {
				t.Fatalf("closed %v, result %+v, trace %v", closed, res, s.trace)
			}
			for _, e := range s.trace {
				if e == "commit done" || e == "commit stage" || e == "merge before deploy" || e == "merge at completion" {
					t.Fatalf("effects = %v", s.trace)
				}
			}
		})
	}
}
