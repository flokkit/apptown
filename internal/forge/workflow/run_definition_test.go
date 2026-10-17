package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
)

func TestPreparedRunFreezesDeclarationAndReceiptBytes(t *testing.T) {
	t.Parallel()
	def := PreparedRun{Spec: schema.Spec{Stages: []schema.Stage{{Name: "first", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}
	config := map[string]any{"precise": json.Number("9007199254740993")}
	def.Spec.Stages[0].Steps[0].With = config
	def.Events = []StartEvent{{Kind: "pin", Payload: json.RawMessage(`{"precise":9007199254740993}`)}}
	frozen, encoded, err := freezeRunDefinition(def)
	if err != nil {
		t.Fatal(err)
	}
	config["precise"] = json.Number("7")
	def.Spec.Stages[0].Steps[0].Name = "changed"
	def.Events[0].Payload[0] = '['
	if got := frozen.Spec.Stages[0].Steps[0]; got.Name != "accept" || got.With["precise"] != json.Number("9007199254740993") {
		t.Fatalf("aliased declaration: %+v", got)
	}
	if string(frozen.Events[0].Payload) != `{"precise":9007199254740993}` {
		t.Fatalf("aliased receipt: %s", frozen.Events[0].Payload)
	}
	if def.Spec.V != 0 || frozen.Spec.V != DefinitionVersion {
		t.Fatal("version stamp mutated caller")
	}
	var stored struct {
		Stages []struct {
			Steps []struct{ With struct{ Precise json.Number } }
		}
	}
	if err := json.Unmarshal(encoded, &stored); err != nil || stored.Stages[0].Steps[0].With.Precise != json.Number("9007199254740993") {
		t.Fatalf("stored precision: %s %v", encoded, err)
	}
}

type mutatingStartScanners struct{ mutate func() }

func (s mutatingStartScanners) SnapshotIn(context.Context, database.Queryer) ([]Scanner, error) {
	s.mutate()
	return []Scanner{{Name: "source", Target: "source"}}, nil
}
func TestRunAndFirstStageUseSameFrozenDeclaration(t *testing.T) {
	t.Parallel()
	owner, req, def := runAdmissionFixture(t)
	db := owner.records.db
	config := map[string]any{"on": "source", "precise": json.Number("9007199254740993")}
	def.Spec.Stages[0].Steps = []schema.Step{{Name: "check", Uses: "scan", With: config}}
	owner = newRunAdmissions(db, NewStages(mutatingStartScanners{mutate: func() { config["precise"] = json.Number("7"); def.Spec.Stages[0].Steps[0].Name = "changed" }}, func(string) bool { return false }), nil)
	run, err := owner.Create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	wantJSONField(t, "definition", json.RawMessage(storedRun(t, db, run.ID).DefinitionJSON), "stages.0.steps.0.name", "check", "stages.0.steps.0.with.precise", "9007199254740993")
	wantStepCount(t, db, 1, func(s *Step) bool {
		return s.RunID == run.ID && s.Name == "check:source" && hasFields(s.Config, "precise", "9007199254740993")
	})
}
