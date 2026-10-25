package project

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServingIdentityIsCapturedAndSeparateFromSettings(t *testing.T) {
	db, id := settingsFixture(t)
	projects := New(db, nil)
	original, err := projects.ByID(t.Context(), id)
	if err != nil || original.ServingLabel != "settings" {
		t.Fatalf("creation did not capture serving label: %+v %v", original, err)
	}
	name := "New display name"
	revision := original.SettingsRevision
	if err := patchSettings(db, id, "settings", SettingsPatch{ExpectedRevision: &revision, Name: &name}); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: simulate a future canonical rename while current settings deliberately refuses slug changes.
	if _, err := db.Exec(`UPDATE projects SET slug='renamed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	got, err := projects.ByServingLabel(t.Context(), "settings")
	if err != nil || got.ID != id || got.Slug != "renamed" || got.ServingLabel != "settings" || got.Name != name {
		t.Fatalf("serving lookup changed: %+v %v", got, err)
	}
	if _, err := projects.ByServingLabel(t.Context(), "renamed"); err == nil {
		t.Fatal("canonical address became an assigned serving label")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ServingLabel") || strings.Contains(string(raw), "servingLabel") || strings.Contains(string(raw), "serving_label") {
		t.Fatalf("private routing field entered settings JSON: %s", raw)
	}
}
