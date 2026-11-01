package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A healthy main must not conceal a broken preview; the open-app address
// still belongs to main even when a different deployment supplies the light.
func TestProjectRuntimeKeepsUrgentDeploymentAndProductionAddress(t *testing.T) {
	id := idstest.N(1)
	for _, state := range []RuntimeState{runtimeUnreachable, RuntimeStopped, RuntimeUnknown} {
		for _, reverse := range []bool{false, true} {
			rows := []runtimeDeployment{
				{ProjectID: id, Name: "main", URL: "https://main.example", Observation: RuntimeObservation{State: RuntimeRunning}},
				{ProjectID: id, Name: "preview", URL: "https://preview.example", Observation: RuntimeObservation{State: state, TargetName: "Preview host"}},
			}
			if reverse {
				rows[0], rows[1] = rows[1], rows[0]
			}
			got := projectRuntimeSummaries(rows)[id]
			if !got.HasProduction || got.State != state || got.TargetName != "Preview host" || got.ProductionURL != "https://main.example" || got.ProductionRunning == nil || !*got.ProductionRunning {
				t.Fatalf("state %v reverse %v: %+v", state, reverse, got)
			}
		}
	}
}
