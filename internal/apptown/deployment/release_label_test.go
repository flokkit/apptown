package deployment

import (
	"encoding/json"
	"testing"
)

func TestReleaseLabelsDistinguishDeploymentRecordFromRuntime(t *testing.T) {
	for _, tc := range []struct {
		state ReleaseState
		label string
	}{{Releasing, "deploying"}, {Released, "deployed"}, {ReleaseFailed, "failed"}} {
		if tc.state.Display().String() != tc.label {
			t.Fatalf("%s: %s", tc.state, tc.state.Display())
		}
		b, err := json.Marshal(tc.state.Display())
		if err != nil {
			t.Fatal(err)
		}
		var decoded ReleaseLabel
		if err := json.Unmarshal(b, &decoded); err != nil || decoded != tc.state.Display() {
			t.Fatalf("label round trip: %s %v", b, err)
		}
	}
}
