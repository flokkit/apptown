package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfferedTargetPreservesZeroRevision(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusOK, OfferedTarget{Name: "new-host", SettingsRevision: 0})
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got["settingsRevision"]) != "0" {
		t.Fatalf("setup needs explicit zero revision; response: %s", w.Body.String())
	}
}
