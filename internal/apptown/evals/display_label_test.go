package evals

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/evals/internal/evalssql"
)

func TestReadersSupplyEvalAndArmLabelsIncludingStoredChecks(t *testing.T) {
	t.Parallel()
	for state, want := range map[string]string{stateRunning: "working", stateStopping: "working", stateHeld: "waiting for you", stateFinished: "finished", stateStopped: "stopped", "": "unknown"} {
		e := evalOf(evalssql.Eval{State: state})
		if got := e.StatusLabel.String(); got != want {
			t.Errorf("eval %s: %s, want %s", state, got, want)
		}
	}
	for state, want := range map[string]string{armQueued: "waiting", armCopying: "working", armCopied: "waiting for you", armStarting: "working", armWorking: "working", armLanding: "working", armScored: "finished", armFailed: "failed", armNotRun: "stopped", "": "unknown"} {
		a := armOf(evalssql.EvalArm{State: state, ScoreJson: []byte(`{"checks":[{"step":"tests","status":"pass"},{"step":"scan","status":"error"}]}`)}, 0, 0)
		if got := a.StatusLabel.String(); got != want {
			t.Errorf("arm %s: %s, want %s", state, got, want)
		}
		if a.Score.Checks[0].StatusLabel.String() != "passed" || a.Score.Checks[1].StatusLabel.String() != "unknown" {
			t.Fatal(a.Score.Checks)
		}
	}
}
