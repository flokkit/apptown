package deploy

import (
	"encoding/json"
	"testing"
)

func TestInventoryLabelsKeepUnknownDistinctFromStopped(t *testing.T) {
	for state, want := range map[string]string{"active": "running", "online": "running", "running": "running", "ready": "passed", "reconciled": "passed", "creating": "working", "reconciling": "working", "rolling": "working", "deploying": "working", "exited": "stopped", "absent": "stopped", "stopped": "stopped", "error": "failed", "failed": "failed", "": "unknown", "future": "unknown"} {
		b, err := json.Marshal(Resource{Status: state})
		var got struct {
			StatusLabel string `json:"statusLabel"`
			Status      string `json:"status"`
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusLabel != want || got.Status != state {
			t.Errorf("%s: %s", state, b)
		}
	}
}
