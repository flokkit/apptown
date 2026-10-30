package operations

import (
	"encoding/json"
	"testing"
)

func TestOperationLabelsDoNotConfuseUnknownWithFailure(t *testing.T) {
	for _, tc := range []struct {
		state OperationState
		want  OperationLabel
	}{{Queued, LabelWaiting}, {Running, LabelWorking}, {Succeeded, LabelFinished}, {Failed, LabelFailed}, {Unknown, LabelUnknown}, {Cancelled, LabelStopped}} {
		raw, _ := json.Marshal(Operation{State: tc.state, Verification: "not-verified"})
		var got struct {
			StatusLabel  OperationLabel
			Verification string
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusLabel != tc.want || got.Verification != "not-verified" {
			t.Fatalf("%s", raw)
		}
	}
}
