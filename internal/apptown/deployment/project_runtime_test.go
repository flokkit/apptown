package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A healthy main must not conceal a broken standing deployment; the open-app address
// still belongs to main even when a different deployment supplies the light.
func TestProjectRuntimeKeepsUrgentDeploymentAndProductionAddress(t *testing.T) {
	id := idstest.N(1)
	for _, state := range []RuntimeState{runtimeUnreachable, RuntimeStopped, RuntimeUnknown} {
		for _, reverse := range []bool{false, true} {
			rows := []runtimeDeployment{
				{ProjectID: id, Kind: Standing, Name: "main", URL: "https://main.example", Observation: RuntimeObservation{State: RuntimeRunning}},
				{ProjectID: id, Kind: Standing, Name: "staging", URL: "https://preview.example", Observation: RuntimeObservation{State: state, TargetName: "Preview host"}},
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

func TestProjectWorksOnlyWhenEveryStandingDeploymentWorks(t *testing.T) {
	id := idstest.N(1)
	good := RuntimeObservation{State: RuntimeRunning, Status: DeploymentStatus{Word: progressLabelWorking}}
	for _, word := range []ProgressLabel{progressLabelUnknown, progressLabelFailed, progressLabelStopped, progressLabelDeploying} {
		for _, reverse := range []bool{false, true} {
			rows := []runtimeDeployment{
				{ProjectID: id, Name: "main", Kind: Standing, Observation: good},
				{ProjectID: id, Name: "staging", Kind: Standing, Observation: RuntimeObservation{State: RuntimeRunning, Status: DeploymentStatus{Word: word, Reason: "storage did not answer in time"}}},
			}
			if reverse {
				rows[0], rows[1] = rows[1], rows[0]
			}
			got := projectRuntimeSummaries(rows)[id].Status
			if got.Word != word || got.Reason != "staging: storage did not answer in time" {
				t.Fatalf("%s reverse %v: %+v", word, reverse, got)
			}
		}
	}
	rows := []runtimeDeployment{{ProjectID: id, Name: "main", Kind: Standing, Observation: good}, {ProjectID: id, Name: "preview", Kind: preview, Observation: RuntimeObservation{State: RuntimeUnknown, Status: UnobservedStatus()}}}
	if got := projectRuntimeSummaries(rows)[id].Status; got.Word != progressLabelWorking {
		t.Fatalf("a preview changed standing health: %+v", got)
	}
}

// A project's word comes from a deployment that runs before one never
// deployed; with only never-deployed deployments it is not deployed.
func TestProjectNotDeployedYieldsToAnyOtherWord(t *testing.T) {
	id := idstest.N(1)
	never := RuntimeObservation{State: RuntimeStopped, Status: DeploymentStatus{Word: progressLabelNotDeployed, Reason: "nothing deployed yet"}}
	good := RuntimeObservation{State: RuntimeRunning, Status: DeploymentStatus{Word: progressLabelWorking}}
	for _, reverse := range []bool{false, true} {
		rows := []runtimeDeployment{{ProjectID: id, Name: "main", Kind: Standing, Observation: never}, {ProjectID: id, Name: "staging", Kind: Standing, Observation: good}}
		if reverse {
			rows[0], rows[1] = rows[1], rows[0]
		}
		if got := projectRuntimeSummaries(rows)[id].Status; got.Word != progressLabelWorking {
			t.Fatalf("reverse %v: %+v", reverse, got)
		}
	}
	rows := []runtimeDeployment{{ProjectID: id, Name: "main", Kind: Standing, Observation: never}}
	if got := projectRuntimeSummaries(rows)[id].Status; got.Word != progressLabelNotDeployed {
		t.Fatalf("only main, never deployed: %+v", got)
	}
}
