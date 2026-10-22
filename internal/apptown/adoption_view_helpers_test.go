package apptown

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/ids"
)

func testProjectPacks(t *testing.T, a *App, id ids.ID) []adoption.ProjectPackRecord {
	t.Helper()
	out, err := adoption.ProjectPacksIn(t.Context(), a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type placementWire struct {
	Packs []struct {
		Pack     string
		Version  int64
		Platform bool
	}
	Additions, Exclusions []adoption.ReceiptBlock
	Conditions            []struct{ Kind, Sentence string }
}

func testPlacement(t *testing.T, a *App, slug string) placementWire {
	t.Helper()
	response := authzCall(a.Handler(), "bls", "GET", "/api/v1/projects/"+slug+"/adoption", "")
	var out placementWire
	if response.Code != 200 {
		t.Fatalf("adoption HTTP %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
