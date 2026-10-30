package evals

import (
	"github.com/flokkit/apptown/internal/apptown/evals/internal/evalssql"
	"testing"
)

func TestReadersSupplyEvalAndArmLabelsIncludingStoredChecks(t *testing.T) {
	t.Parallel()
	for state, want := range map[string]string{StateRunning: "working", StateStopping: "working", StateHeld: "waiting for you", StateFinished: "finished", StateStopped: "stopped", "": "unknown"} {
		e := evalOf(evalssql.Eval{State: state})
		if got := e.StatusLabel.String(); got != want {
			t.Errorf("eval %s: %s, want %s", state, got, want)
		}
	}
	for state, want := range map[string]string{ArmQueued: "waiting", ArmCopying: "working", ArmCopied: "waiting for you", ArmStarting: "working", ArmWorking: "working", ArmLanding: "working", ArmScored: "finished", ArmFailed: "failed", ArmNotRun: "stopped", "": "unknown"} {
		a := armOf(evalssql.EvalArm{State: state, ScoreJson: []byte(`{"checks":[{"step":"tests","status":"pass"},{"step":"scan","status":"error"}]}`)}, 0, 0)
		if got := a.StatusLabel.String(); got != want {
			t.Errorf("arm %s: %s, want %s", state, got, want)
		}
		if a.Score.Checks[0].StatusLabel.String() != "passed" || a.Score.Checks[1].StatusLabel.String() != "unknown" {
			t.Fatal(a.Score.Checks)
		}
	}
}
