package deploy

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"strings"
	"testing"
)

func TestProbeRuntimeTokenCannotBeRestoredFromRecordedObservation(t *testing.T) {
	raw, err := json.Marshal(Observation{RuntimeToken: "live-container/start-time", ReadinessPolicy: &appcontract.HealthPolicy{Explicit: true}, Running: "retained-provider-handle"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "live-container") || strings.Contains(string(raw), "Explicit") {
		t.Fatal("ephemeral probe identity entered a durable observation")
	}
	var got Observation
	if err := json.Unmarshal([]byte(`{"ReadinessPolicy":{"Explicit":true},"runtimeToken":"supplied","RuntimeToken":"other-supplied","running":"retained-provider-handle"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.RuntimeToken != "" || got.ReadinessPolicy != nil || got.Running != "retained-provider-handle" {
		t.Fatalf("recorded observation supplied current probe identity: %+v", got)
	}
}
