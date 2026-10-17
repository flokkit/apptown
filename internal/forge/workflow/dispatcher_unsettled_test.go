package workflow

import (
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A task runner can return with its step still running when Finish fails.
// Its dependent entry steps must wait for durable settlement.
func TestAStepLeftRunningByItsRunnerHoldsTheStepsAfterIt(t *testing.T) {
	d, s := testDispatcher([]ReadyStep{{idstest.N(1), "script", true, "build"}, {idstest.N(2), "deploy", true, "deploy"}})
	d.runners["script"] = func(id ids.ID) { s.ran(id, StatusRunning) }
	d.Dispatch(s.p, false)
	d.Wait()
	if len(s.calls) != 1 || s.advances != 0 {
		t.Fatalf("runner left build running, but calls=%v advances=%d", s.calls, s.advances)
	}
}
