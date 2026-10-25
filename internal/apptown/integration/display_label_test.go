package integration

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestConnectionLabelPreservesConfigurationAndVersion(t *testing.T) {
	db := testDB(t)
	bindings := NewWithClock(db, nil)
	if _, err := bindings.Save(t.Context(), "customer-provider", "deploy", "portainer", `{"urlFrom":"URL","apiKeyFrom":"KEY"}`, "alice"); err != nil {
		t.Fatal(err)
	}
	before, err := bindings.ByName(t.Context(), "customer-provider")
	if err != nil {
		t.Fatal(err)
	}
	if before.DisplayLabel != before.Name {
		t.Fatal("new connection label not initialized")
	}
	changed, err := bindings.EditDisplayLabel(t.Context(), before.ID, before.DisplayLabel, "Customer services", "alice")
	if err != nil {
		t.Fatal(err)
	}
	expected := *before
	expected.DisplayLabel = "Customer services"
	if !reflect.DeepEqual(*changed, expected) {
		t.Fatalf("label changed credential identity/configuration: %+v %+v", expected, changed)
	}
	dbtest.FailNextStatement(t, db, "-- name: AppendReceipt")
	if _, err := bindings.EditDisplayLabel(t.Context(), before.ID, changed.DisplayLabel, "unrecorded", "alice"); err == nil {
		t.Fatal("failed receipt accepted")
	}
	saved, err := bindings.ByName(t.Context(), before.Name)
	if err != nil || !reflect.DeepEqual(saved, changed) {
		t.Fatalf("label escaped failed receipt: %+v %v", saved, err)
	}
	if _, err := bindings.Save(t.Context(), before.Name, "deploy", "portainer", `{"urlFrom":"URL","apiKeyFrom":"ROTATED_KEY"}`, "alice"); err != nil {
		t.Fatal(err)
	}
	saved, err = bindings.ByName(t.Context(), before.Name)
	if err != nil || saved.DisplayLabel != changed.DisplayLabel || saved.ID != before.ID {
		t.Fatalf("credential rotation reset label/identity: %+v %v", saved, err)
	}
	if _, err := bindings.EditDisplayLabel(t.Context(), before.ID, before.DisplayLabel, "stale", "alice"); err == nil {
		t.Fatal("stale label edit accepted")
	}
}
