package workflow

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestStepDecisionSnapshotRejectsEachChangedAdmissionFact(t *testing.T) {
	t.Parallel()
	originalRun := Run{ID: idstest.N(2), ProjectID: idstest.N(3), ChangeID: idstest.N(4), RetryRevision: 6, CurrentStage: "build"}
	originalStep := Step{ID: idstest.N(1), RunID: idstest.N(2), Attempt: 5, Type: "agent/pi", Config: json.RawMessage(`{"limit":9007199254740993}`)}
	observed := stepDecisionSnapshot{scope: decisionScope{StepID: idstest.N(1), RunID: idstest.N(2), ProjectID: idstest.N(3), ChangeID: idstest.N(4)}, attempt: 5, revision: 6, stage: "build", stepType: "agent/pi", config: append(json.RawMessage(nil), originalStep.Config...)}
	if !observed.matches(&originalRun, &originalStep) {
		t.Fatal("unchanged observation rejected")
	}
	cases := map[string]func(*Run, *Step){
		"step":           func(_ *Run, s *Step) { s.ID = idstest.N(999999) },
		"run":            func(r *Run, _ *Step) { r.ID = idstest.N(999999) },
		"project":        func(r *Run, _ *Step) { r.ProjectID = idstest.N(999999) },
		"change":         func(r *Run, _ *Step) { r.ChangeID = idstest.N(999999) },
		"attempt":        func(_ *Run, s *Step) { s.Attempt++ },
		"retry revision": func(r *Run, _ *Step) { r.RetryRevision++ },
		"stage":          func(r *Run, _ *Step) { r.CurrentStage = "ship" },
		"kind":           func(_ *Run, s *Step) { s.Type = "approval" },
		"config":         func(_ *Run, s *Step) { s.Config = json.RawMessage(`{"limit":9007199254740992}`) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			run, step := originalRun, originalStep
			change(&run, &step)
			if observed.matches(&run, &step) {
				t.Fatal("changed admission fact accepted")
			}
		})
	}
	// A concurrent vote must be evaluated from fresh evidence under the lock;
	// it must not be rejected by an unrelated command's admission snapshot.
	run, step := originalRun, originalStep
	step.Evidence = json.RawMessage(`{"approvals":["another-reviewer"]}`)
	step.Status = StatusNeedsHuman
	if !observed.matches(&run, &step) {
		t.Fatal("current voting evidence invalidated admission")
	}
}
